import React from 'react'
import ReactDOM from 'react-dom/client'
import './theme/tokens.css'
import { ThemeProvider } from './theme/ThemeProvider'
import App from './App'
import { LogsWindow } from './app/LogsWindow'
import { initZoom } from './app/zoom'

// isLogsWindow detects the pop-out log window boot mode from a query string.
// Extracted as a pure helper so the branch is unit-testable (the main.tsx side
// effect itself is not).
export function isLogsWindow(search: string): boolean {
  return new URLSearchParams(search).get('logswin') === '1'
}

const popout = isLogsWindow(window.location.search)

// App zoom applies to both roots, before first paint so a zoomed session never
// flashes at 100%.
initZoom()

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <ThemeProvider>
      {popout ? <LogsWindow /> : <App />}
    </ThemeProvider>
  </React.StrictMode>,
)
