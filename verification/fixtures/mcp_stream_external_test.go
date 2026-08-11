//go:build mcpstreamoverlay

package mcp

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/agentcell/agentcell-client/contract"
)

type blockingLogOperations struct {
	firstEmitted chan struct{}
	release      chan struct{}
}

func (*blockingLogOperations) Execute(context.Context, contract.Request) (contract.Response, error) {
	return nil, nil
}
func (operations *blockingLogOperations) Stream(_ context.Context, _ contract.Request, emit func(contract.Response) error) error {
	if err := emit(&contract.LogEntry{Time: "t1", Stream: "stdout", Message: "first"}); err != nil {
		return err
	}
	close(operations.firstEmitted)
	<-operations.release
	return emit(&contract.LogEntry{Time: "t2", Stream: "stdout", Message: "last"})
}

type notifyingBuffer struct {
	bytes.Buffer
	once  sync.Once
	wrote chan struct{}
}

func (buffer *notifyingBuffer) Write(data []byte) (int, error) {
	buffer.once.Do(func() { close(buffer.wrote) })
	return buffer.Buffer.Write(data)
}

func TestMCPLogsBuffersUntilTheSourceStreamEnds(t *testing.T) {
	operations := &blockingLogOperations{firstEmitted: make(chan struct{}), release: make(chan struct{})}
	output := &notifyingBuffer{wrote: make(chan struct{})}
	done := make(chan error, 1)
	input := bytes.NewBufferString("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"logs\",\"arguments\":{\"cell\":\"cell-a\",\"follow\":true}}}\n")
	go func() { done <- (Server{Operations: operations}).Serve(context.Background(), input, output) }()
	<-operations.firstEmitted
	select {
	case <-output.wrote:
		t.Fatal("MCP unexpectedly wrote a response before the log stream ended")
	case <-time.After(150 * time.Millisecond):
		t.Log("observed no MCP output after the first log record; response is buffered")
	}
	close(operations.release)
	select {
	case <-output.wrote:
	case <-time.After(time.Second):
		t.Fatal("MCP did not respond after the stream ended")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte("first")) || !bytes.Contains(output.Bytes(), []byte("last")) {
		t.Fatalf("response = %s", output.Bytes())
	}
}
