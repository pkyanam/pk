// Command fake-agent is a no-credentials ACP v1 fixture for the official SDK smoke.
package main

import (
	"context"
	"os"

	"github.com/pkyanam/pk/internal/acp"
)

func main() {
	server, err := acp.NewServer(os.Stdin, os.Stdout, acp.Config{
		Run: func(ctx context.Context, turn acp.Turn, emit func(acp.Update) error) (acp.TurnResult, error) {
			if err := emit(acp.Update{Kind: "assistant", Text: "SDK fixture reply"}); err != nil {
				return acp.TurnResult{}, err
			}
			return acp.TurnResult{}, nil
		},
	})
	if err != nil {
		panic(err)
	}
	_ = server.Serve(context.Background())
}
