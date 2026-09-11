import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { initLocale } from './i18n'
import './styles.css'

// THE ONE PLACE the console decides which language it is in, before the first
// render. Only a registered locale can win and English is the only catalogue
// shipped, so this resolves to English today and needs no change when a
// second one is added.
initLocale()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
