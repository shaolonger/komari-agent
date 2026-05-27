package terminal

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

const defaultMaxTerminalSessions = 1
const defaultTerminalIdleTimeout = 5 * time.Minute
const defaultTerminalMaxDuration = 30 * time.Minute

var terminalSessionSlotsMu sync.Mutex
var terminalSessionSlots chan struct{}
var terminalSessionSlotsLimit int

// Terminal 接口定义平台特定的终端操作
type Terminal interface {
	Close() error
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait() error
}

// terminalImpl 封装终端和平台特定逻辑
type terminalImpl struct {
	shell      string
	workingDir string
	term       Terminal
}

// StartTerminal 启动终端并处理 WebSocket 通信
func StartTerminal(conn *websocket.Conn) {
	if !terminalCapabilityEnabled() {
		conn.WriteMessage(websocket.TextMessage, []byte("\n\nTerminal access is disabled. Enable it explicitly with --enable-terminal or --enable-remote-control if required."))
		conn.Close()
		return
	}
	releaseTerminalSession, ok := acquireTerminalSessionSlot()
	if !ok {
		conn.WriteMessage(websocket.TextMessage, []byte("\n\nTerminal session rejected because the per-agent terminal session limit has been reached."))
		conn.Close()
		return
	}
	defer releaseTerminalSession()

	impl, err := newTerminalImpl()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v\r\n", err)))
		return
	}

	errChan := make(chan error, 3) // 增加容量以容纳多个错误源
	activity := make(chan struct{}, 1)
	done := make(chan struct{})
	idleTimer := time.NewTimer(terminalIdleTimeout())
	defer idleTimer.Stop()
	maxDurationTimer := time.NewTimer(terminalMaxDuration())
	defer maxDurationTimer.Stop()
	signalTerminalActivity(activity)

	defer func() {
		gracefulShutdown(impl.term)
		impl.term.Close()
		conn.Close()
		close(done)
	}()

	// 从 WebSocket 读取消息并写入终端
	go handleWebSocketInput(conn, impl.term, errChan, done, activity)

	// 从终端读取输出并写入 WebSocket
	go handleTerminalOutput(conn, impl.term, errChan, done, activity)

	// 等待终端进程结束、会话超时或出现错误
	for {
		select {
		case err := <-errChan:
			if err != nil {
				conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\nConnection error: %v\r\n", err)))
			}
			return
		case <-activity:
			resetTimer(idleTimer, terminalIdleTimeout())
		case <-idleTimer.C:
			conn.WriteMessage(websocket.TextMessage, []byte("\r\nTerminal session closed due to idle timeout.\r\n"))
			return
		case <-maxDurationTimer.C:
			conn.WriteMessage(websocket.TextMessage, []byte("\r\nTerminal session closed due to maximum session duration.\r\n"))
			return
		case <-done:
			return
		}
	}
}

func terminalCapabilityEnabled() bool {
	return flags.TerminalEnabled()
}

func terminalSessionLimit() int {
	if flags.MaxTerminalSessions > 0 {
		return flags.MaxTerminalSessions
	}
	return defaultMaxTerminalSessions
}

func terminalIdleTimeout() time.Duration {
	if flags.TerminalIdleTimeout > 0 {
		return time.Duration(flags.TerminalIdleTimeout) * time.Second
	}
	return defaultTerminalIdleTimeout
}

func terminalMaxDuration() time.Duration {
	if flags.TerminalMaxDuration > 0 {
		return time.Duration(flags.TerminalMaxDuration) * time.Second
	}
	return defaultTerminalMaxDuration
}

func acquireTerminalSessionSlot() (func(), bool) {
	limit := terminalSessionLimit()

	terminalSessionSlotsMu.Lock()
	if terminalSessionSlots == nil || terminalSessionSlotsLimit != limit {
		terminalSessionSlots = make(chan struct{}, limit)
		terminalSessionSlotsLimit = limit
	}
	slots := terminalSessionSlots
	terminalSessionSlotsMu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() {
			<-slots
		}, true
	default:
		return func() {}, false
	}
}

func signalTerminalActivity(activity chan<- struct{}) {
	select {
	case activity <- struct{}{}:
	default:
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

// gracefulShutdown 尝试优雅地关闭终端
func gracefulShutdown(term Terminal) {
	//  Ctrl+C
	for i := 0; i < 3; i++ {
		if _, err := term.Write([]byte{3}); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)

	//  Ctrl+D (EOF)
	term.Write([]byte{4})
	time.Sleep(100 * time.Millisecond)

	term.Write([]byte("exit\n"))
	time.Sleep(100 * time.Millisecond)
}

// handleWebSocketInput 处理 WebSocket 输入
func handleWebSocketInput(conn *websocket.Conn, term Terminal, errChan chan<- error, done <-chan struct{}, activity chan<- struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}

		t, p, err := conn.ReadMessage()
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
			return
		}
		signalTerminalActivity(activity)
		if t == websocket.TextMessage {
			var cmd struct {
				Type  string `json:"type"`
				Cols  int    `json:"cols,omitempty"`
				Rows  int    `json:"rows,omitempty"`
				Input string `json:"input,omitempty"`
			}
			if err := json.Unmarshal(p, &cmd); err == nil {
				switch cmd.Type {
				case "resize":
					if cmd.Cols > 0 && cmd.Rows > 0 {
						term.Resize(cmd.Cols, cmd.Rows)
					}
				case "input":
					if cmd.Input != "" {
						term.Write([]byte(cmd.Input))
					}
				}
			} else {
				term.Write(p)
			}
		}
		if t == websocket.BinaryMessage {
			term.Write(p)
		}
	}
}

// handleTerminalOutput 处理终端输出
func handleTerminalOutput(conn *websocket.Conn, term Terminal, errChan chan<- error, done <-chan struct{}, activity chan<- struct{}) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-done:
			return
		default:
		}

		n, err := term.Read(buf)
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
			select {
			case errChan <- err:
			default:
			}
			return
		}
		signalTerminalActivity(activity)
	}
}
