import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ErrorBoundary } from "react-error-boundary";

import App from './App.tsx'
import { ErrorFallback } from './ErrorFallback.tsx'
import { loadConfig } from './lib/frame-config.ts'

import "./main.css"
import "./styles/theme.css"
import "./index.css"

// Config first: every screen's SDK call reads namespaces and ports out of it,
// so rendering before it lands would fire the first round of requests against
// the compiled defaults. loadConfig never rejects — it falls back to those
// defaults and records why, which the Settings screen shows.
loadConfig().then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <ErrorBoundary FallbackComponent={ErrorFallback}>
        <App />
      </ErrorBoundary>
    </StrictMode>
  )
})
