import { Component, StrictMode } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'

class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('[UI] render failed', { errorType: error.name, componentStack: info.componentStack })
  }

  render() {
    if (this.state.failed) {
      return <main><section className="card"><h1>Something went wrong</h1>
        <p>Unable to display this page. Check the browser console for details, then reload.</p>
        <button onClick={() => window.location.reload()}>Reload page</button>
      </section></main>
    }
    return this.props.children
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary><App /></ErrorBoundary>
  </StrictMode>,
)
