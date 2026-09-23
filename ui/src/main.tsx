import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { AppBoundary } from './components/ErrorBoundary'
import { installChunkReload } from './lib/chunkReload'

// Before the first render: a tab left open across an upgrade fails on its first
// lazy route, and the cure is loading the new index. See lib/chunkReload.ts.
installChunkReload()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AppBoundary>
      <App />
    </AppBoundary>
  </StrictMode>,
)
