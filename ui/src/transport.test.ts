import { describe, expect, test } from "bun:test"
import { PkTransport } from "./transport"
import type { ServerEvent } from "./protocol"

describe("RPC transport stream lifecycle", () => {
  test("announces closure after a failed event stream", async () => {
    const events: ServerEvent[] = []
    const transport = new PkTransport((event) => events.push(event))
    const stream = new ReadableStream<Uint8Array>({
      start(controller) { controller.error(new Error("broken pipe")) },
    })

    await (transport as any).readEvents(stream)

    expect(events.map((event) => event.type)).toEqual(["error", "rpc_closed"])
    expect(events[0]?.payload?.message).toContain("broken pipe")
  })

  test("announces closure after clean end of the event stream", async () => {
    const events: ServerEvent[] = []
    const transport = new PkTransport((event) => events.push(event))
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('{"version":1,"type":"ready"}\n'))
        controller.close()
      },
    })

    await (transport as any).readEvents(stream)

    expect(events.map((event) => event.type)).toEqual(["ready", "rpc_closed"])
  })
})
