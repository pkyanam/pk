package main

import (
	"context"
	"fmt"
	"io"

	"github.com/pkyanam/pk/internal/extensions"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type cliRegistryExtension struct {
	Decorate          func(tool.Registry) tool.Registry
	RemoteJobHandlers func(context.Context) []operation.RemoteJobHandler
}

func configureCLIExtensions(ctx context.Context, options *runner.Options, manifestPaths []string, factory extensions.WorkerFactory, diagnostics io.Writer, extra ...cliRegistryExtension) (*extensions.Host, error) {
	return configureCLIExtensionsWithLoadNotices(ctx, options, manifestPaths, factory, diagnostics, true, extra...)
}

// configureCLIExtensionsQuiet keeps repeated child startup chatter out of the
// parent transcript while still reporting invalid manifests and disabled
// extensions. It does not change which extensions are loaded.
func configureCLIExtensionsQuiet(ctx context.Context, options *runner.Options, manifestPaths []string, factory extensions.WorkerFactory, diagnostics io.Writer, extra ...cliRegistryExtension) (*extensions.Host, error) {
	return configureCLIExtensionsWithLoadNotices(ctx, options, manifestPaths, factory, diagnostics, false, extra...)
}

func configureCLIExtensionsWithLoadNotices(ctx context.Context, options *runner.Options, manifestPaths []string, factory extensions.WorkerFactory, diagnostics io.Writer, showLoadNotices bool, extra ...cliRegistryExtension) (*extensions.Host, error) {
	var host *extensions.Host
	var decorators []func(tool.Registry) tool.Registry
	var handlerFactories []func(context.Context) []operation.RemoteJobHandler
	if len(manifestPaths) > 0 {
		manifests, issues := extensions.LoadManifests(manifestPaths)
		for _, issue := range issues {
			fmt.Fprintf(diagnostics, "pk: extension warning: %v\n", issue)
		}
		var report extensions.Report
		var err error
		host, report, err = extensions.NewHost(ctx, options.Workspace, manifests, factory)
		if err != nil {
			return nil, err
		}
		if showLoadNotices {
			for _, id := range report.Loaded {
				fmt.Fprintf(diagnostics, "pk: loaded extension %s\n", id)
			}
		}
		for _, issue := range report.Disabled {
			fmt.Fprintf(diagnostics, "pk: extension warning: %v\n", issue)
		}
		decorators = append(decorators, func(base tool.Registry) tool.Registry {
			decorated, decoratorIssues := extensions.DecorateRegistry(base, host)
			for _, issue := range decoratorIssues {
				fmt.Fprintf(diagnostics, "pk: extension warning: %v\n", issue)
			}
			return decorated
		})
		handlerFactories = append(handlerFactories, host.RemoteJobHandlers)
	}
	for _, extension := range extra {
		if extension.Decorate != nil {
			decorators = append(decorators, extension.Decorate)
		}
		if extension.RemoteJobHandlers != nil {
			handlerFactories = append(handlerFactories, extension.RemoteJobHandlers)
		}
	}
	options.DecorateRegistry = composeRegistryDecorators(options.DecorateRegistry, decorators...)
	options.RemoteJobHandlers = composeRemoteJobHandlers(options.RemoteJobHandlers, handlerFactories...)
	return host, nil
}

func composeRegistryDecorators(existing func(tool.Registry) tool.Registry, decorators ...func(tool.Registry) tool.Registry) func(tool.Registry) tool.Registry {
	if existing == nil && len(decorators) == 0 {
		return nil
	}
	return func(base tool.Registry) tool.Registry {
		if existing != nil {
			base = existing(base)
		}
		for _, decorate := range decorators {
			if decorate != nil {
				base = decorate(base)
			}
		}
		return base
	}
}

func composeRemoteJobHandlers(existing func(context.Context) []operation.RemoteJobHandler, factories ...func(context.Context) []operation.RemoteJobHandler) func(context.Context) []operation.RemoteJobHandler {
	if existing == nil && len(factories) == 0 {
		return nil
	}
	return func(ctx context.Context) []operation.RemoteJobHandler {
		var handlers []operation.RemoteJobHandler
		if existing != nil {
			handlers = append(handlers, existing(ctx)...)
		}
		for _, factory := range factories {
			if factory != nil {
				handlers = append(handlers, factory(ctx)...)
			}
		}
		return handlers
	}
}
