import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ErrorBoundary } from "react-error-boundary";

import App from './App.tsx'
import { ErrorFallback } from './ErrorFallback.tsx'

import "./main.css"
import "./styles/theme.css"
import "./index.css"

// `loadConfig()` used to run here, before `<App/>` mounted and so outside the
// session gate. It reads a ConfigMap through the apiserver, and before anyone
// has signed in that request has no bearer token: it 401s at the uiproxy on
// every page load, including the load that renders the login screen. Pre-branch
// it was a harmless anonymous read; now it is a guaranteed failure whose only
// visible effect is that the console silently runs on DEFAULT_CONFIG.
//
// It moved into `App`, immediately after the session is confirmed and before
// any screen mounts — which is the property the old comment was protecting
// (no SDK call may fire against the compiled defaults), just enforced one
// step later in the sequence.
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary FallbackComponent={ErrorFallback}>
      <App />
    </ErrorBoundary>
  </StrictMode>
)
