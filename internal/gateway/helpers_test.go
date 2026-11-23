package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/session"
	"github.com/olegpustovit/agentpack/internal/state/statetest"
	"golang.org/x/term"
)

// Test helpers shared with internal/network tests; a test binary can only re-exec its own children.

func TestTerminalChild(t *testing.T) {
	if os.Getenv("AGENTPACK_TEST_TERMINAL_CHILD") != "1" {
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		os.Exit(91)
	}
	if os.Getenv("AGENTPACK_TEST_TERMINAL_DA") == "1" {
		restore, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			os.Exit(93)
		}
		fmt.Print("\x1b[c")
		reply := make([]byte, 0, 16)
		for buffer := make([]byte, 1); !strings.HasSuffix(string(reply), "c"); {
			if _, err := os.Stdin.Read(buffer); err != nil {
				os.Exit(94)
			}
			reply = append(reply, buffer[0])
		}
		_ = term.Restore(int(os.Stdin.Fd()), restore)
		fmt.Printf("DA %q\n", reply)
		os.Exit(0)
	}
	fmt.Print("\x1b[2JREADY\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "trace":
			fmt.Println("TRACE", os.Getenv("AGENTPACK_TRACE"))
		case "size":
			columns, rows, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				os.Exit(92)
			}
			fmt.Printf("SIZE %d %d\n", rows, columns)
		case "exit":
			os.Exit(7)
		default:
			fmt.Printf("ECHO %s\n", scanner.Text())
		}
	}
	os.Exit(0)
}

func testSession(t *testing.T) *network.Session {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestTerminalChild$")
	command.Env = append(os.Environ(), "AGENTPACK_TEST_TERMINAL_CHILD=1")
	session, err := network.StartSession(command, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Stop(ctx)
		wait, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if _, err := session.Wait(wait); err != nil {
			t.Error(err)
		}
	})
	return session
}

func readWorker(t *testing.T, connection *websocket.Conn, matches func(int, []byte) bool) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		kind, data, err := connection.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if matches(kind, data) {
			return
		}
	}
}

func testRememberACPConversationOwner(t *testing.T, home string, info session.SessionInfo, fallback string) error {
	t.Helper()
	store := statetest.Open(t, home)
	if info.ID == "" {
		info.ID = "session-" + rand.Text()
	}
	pending := info
	pending.AgentSessionID = ""
	if err := session.UpsertSession(t.Context(), store.DB, pending); err != nil {
		return err
	}
	return network.RememberACPConversationOwner(t.Context(), store, info, fallback)
}
