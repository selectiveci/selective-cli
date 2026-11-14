package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func fakeRunner() int {
	if os.Getenv("FAKE_IGNORE_SIGINT") == "1" {
		signal.Ignore(syscall.SIGINT)
	}
	fmt.Println("fake runner stdout noise")
	fmt.Fprintln(os.Stderr, "fake runner stderr noise")
	if path := os.Getenv("FAKE_ARGS_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Args[1:], " ")), 0o644)
	}
	if os.Getenv("FAKE_NEVER_CONNECT") == "1" {
		time.Sleep(time.Minute)
		return 0
	}
	transport := exec.Command(os.Getenv("SELECTIVE_TRANSPORT"), "--stdio")
	in, _ := transport.StdinPipe()
	out, _ := transport.StdoutPipe()
	transport.Stderr = os.Stderr
	if transport.Start() != nil {
		return 1
	}
	send := func(kind string, data map[string]any) {
		line, _ := json.Marshal(map[string]any{"type": kind, "data": data})
		_, _ = in.Write(append(line, '\n'))
	}
	commands := bufio.NewScanner(out)
	commands.Buffer(nil, 1<<20)
	for commands.Scan() {
		var command struct {
			Command string   `json:"command"`
			IDs     []string `json:"test_case_ids"`
		}
		_ = json.Unmarshal(commands.Bytes(), &command)
		switch command.Command {
		case "test_manifest":
			send("test_manifest", manifest())
		case "run_test_cases":
			if os.Getenv("FAKE_HANG") == "1" {
				time.Sleep(time.Minute)
			}
			record(command.IDs)
			for _, id := range command.IDs {
				send("test_case_result", result(id))
			}
		case "close":
			send("report_at_finish", map[string]any{"connection_retries": 0})
			if os.Getenv("FAKE_LINGER") == "1" {
				time.Sleep(time.Minute)
			}
			_, _ = io.WriteString(in, "exit\n")
			_ = transport.Wait()
			return 0
		}
	}
	return 1
}

func manifest() map[string]any {
	var cases []map[string]any
	for _, id := range strings.Split(os.Getenv("FAKE_IDS"), ",") {
		file, _, _ := strings.Cut(id, "::")
		cases = append(cases, map[string]any{"id": id, "file_path": file, "line_number": 1})
	}
	return map[string]any{
		"test_cases":    cases,
		"test_map_tree": map[string]any{"sha": "abc", "prefix": "", "files": map[string]string{}, "dirty": []string{}},
	}
}

func result(id string) map[string]any {
	file, _, _ := strings.Cut(id, "::")
	data := map[string]any{"id": id, "status": "passed", "file_path": file, "line_number": 1, "run_time": 0.01}
	if id == os.Getenv("FAKE_FAIL") {
		data["status"] = "failed"
		data["failure_message_lines"] = []string{"expected 1", "got 2"}
	}
	return data
}

func record(ids []string) {
	if path := os.Getenv("FAKE_RAN_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(ids, "\n")+"\n"), 0o644)
	}
}
