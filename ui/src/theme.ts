import { useSyncExternalStore } from "react"

export type ThemeID = "dark-mint" | "light" | "high-contrast"
export type ThemePalette = {
  bg: string
  panel: string
  raised: string
  line: string
  text: string
  muted: string
  dim: string
  accent: string
  blue: string
  amber: string
  red: string
  green: string
  purple: string
}

export const themeDefinitions: Record<ThemeID, { label: string; description: string; palette: ThemePalette }> = {
  "dark-mint": {
    label: "Dark Mint",
    description: "The default dark workspace with soft mint accents.",
    palette: {
      bg: "#111315", panel: "#181b1e", raised: "#202428", line: "#2b3035",
      text: "#e5e8eb", muted: "#858e96", dim: "#5d666e", accent: "#8ab4a1",
      blue: "#9db8d8", amber: "#d3ac72", red: "#d88787", green: "#a8cf83", purple: "#bf9ad9",
    },
  },
  light: {
    label: "Light",
    description: "A bright neutral palette with clear accent colors.",
    palette: {
      bg: "#f4f6f7", panel: "#ffffff", raised: "#e8edf0", line: "#c8d0d5",
      text: "#20262b", muted: "#56616a", dim: "#606b73", accent: "#167653",
      blue: "#165ca5", amber: "#875700", red: "#ad2727", green: "#397300", purple: "#7041a0",
    },
  },
  "high-contrast": {
    label: "High Contrast",
    description: "Black surfaces, bright text, and stronger state colors.",
    palette: {
      bg: "#000000", panel: "#080808", raised: "#171717", line: "#ffffff",
      text: "#ffffff", muted: "#e4e4e4", dim: "#c4c4c4", accent: "#00ff9c",
      blue: "#80d8ff", amber: "#ffe600", red: "#ff6961", green: "#b5ff00", purple: "#e2a8ff",
    },
  },
}

let activeTheme: ThemeID = "dark-mint"
export const palette: ThemePalette = { ...themeDefinitions[activeTheme].palette }
const listeners = new Set<() => void>()

export function getThemeID(): ThemeID {
  return activeTheme
}

export function isThemeID(value: unknown): value is ThemeID {
  return typeof value === "string" && Object.hasOwn(themeDefinitions, value)
}

export function applyTheme(id: ThemeID): void {
  if (id === activeTheme) return
  activeTheme = id
  Object.assign(palette, themeDefinitions[id].palette)
  for (const listener of listeners) listener()
}

export function useThemePalette(): ThemePalette {
  useSyncExternalStore((listener) => {
    listeners.add(listener)
    return () => listeners.delete(listener)
  }, getThemeID, getThemeID)
  return palette
}
