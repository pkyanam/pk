package main

import (
	"context"
	"fmt"
	"io"

	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// configureCLIMCP loads only user-selected MCP servers. The caller owns and
// must close the returned host after runner.Run settles.
func configureCLIMCP(ctx context.Context, options *runner.Options, diagnostics io.Writer) (*mcpclient.Host, error) {
	if options == nil {
		return nil, fmt.Errorf("runner options are required")
	}
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	servers, err := (mcpclient.ConfigStore{Home: pkHome()}).List()
	if err != nil {
		return nil, fmt.Errorf("load MCP configuration: %w", err)
	}
	if len(servers) == 0 {
		options.MCPFingerprint = ""
		return nil, nil
	}
	host, report, err := mcpclient.NewHost(ctx, servers)
	if err != nil {
		return nil, err
	}
	for _, id := range report.Loaded {
		fmt.Fprintf(diagnostics, "pk: loaded MCP server %s\n", id)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(diagnostics, "pk: MCP warning: %v\n", warning)
	}
	extra := cliRegistryExtension{
		Decorate: func(base tool.Registry) tool.Registry {
			decorated, issues := mcpclient.DecorateRegistry(base, host)
			for _, issue := range issues {
				fmt.Fprintf(diagnostics, "pk: MCP warning: %v\n", issue)
			}
			return decorated
		},
		RemoteJobHandlers: host.RemoteJobHandlers,
	}
	if _, err := configureCLIExtensions(ctx, options, nil, nil, diagnostics, extra); err != nil {
		_ = host.Close()
		return nil, err
	}
	options.MCPFingerprint = host.SchemaFingerprint()
	return host, nil
}
