# Unreal public-package composition spike

This isolated Go module pins Unreal Agent v0.1.1, commit `b7c9bf1c5c2fa4127255c07727a7c8413e23944a`. It is an exploratory compatibility check, not the pk runtime.

```sh
cd experiments/unreal-composition
go test -race ./...
```

Requires Go 1.27. The first run may download its toolchain and dependencies.

The tests prove that an external module can construct the public provider client without importing upstream internals, and that its context builder preserves a submitted prefix when a running tool later completes. They use no real credentials, perform no model requests, and execute no shell tool. They do not prove end-to-end coordinator composition, cross-provider acceptance of the async result protocol, or measured cost savings.
