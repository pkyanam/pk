import { createCliRenderer, getTreeSitterClient } from "@opentui/core"
import { createRoot } from "@opentui/react"
import { PkApp } from "./app"
import { PkTransport } from "./transport"

const workspace = process.env.PK_WORKSPACE || process.cwd()
const session = process.env.PK_SESSION || undefined
const markdownClient = getTreeSitterClient()
await markdownClient.initialize()
await markdownClient.preloadParser("markdown")
await markdownClient.preloadParser("markdown_inline")
const renderer = await createCliRenderer({
  exitOnCtrlC: false,
  targetFps: 30,
  useMouse: true,
  useKittyKeyboard: { disambiguate: true },
  autoFocus: false,
})
const transport = new PkTransport()
createRoot(renderer).render(<PkApp transport={transport} workspace={workspace} initialSession={session} />)

let closing = false
renderer.once("destroy", () => {
  if (closing) return
  closing = true
  void transport.close()
})
