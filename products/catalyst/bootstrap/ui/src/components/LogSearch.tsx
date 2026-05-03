/**
 * LogSearch — search bar embedded in the LogPane header.
 *
 * Features:
 *   • free-text search with case-insensitive substring match
 *   • optional regex mode (toggle pill)
 *   • level filter chips (INFO / WARN / ERROR / DEBUG) — multi-select,
 *     applied as an AND filter against the text query
 *   • match count "<n> of <m>"
 *   • prev/next navigation (n/N) — wraps around
 *
 * The component is purely presentational: it owns the input value +
 * the level-chip selection, and emits a `LogFilter` shape every time
 * the operator changes anything. The LogPane consumer applies the
 * filter to its line list and feeds back the live match count + the
 * currently-focused match index for the n/N controls.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the level
 * vocabulary mirrors {@link LOG_LEVELS} below; consumers MUST NOT
 * inline level strings.
 */

import { useCallback, useMemo, useState } from 'react'
import type { LogLevel } from './ExecutionLogs'

export const LOG_LEVELS: readonly LogLevel[] = ['INFO', 'WARN', 'ERROR', 'DEBUG']

/** Live filter shape — emitted on every change. The consumer applies
 *  it to the line list. */
export interface LogFilter {
  /** Free-text query — empty string means "no text filter". */
  query: string
  /** Treat `query` as a regex when true. */
  regex: boolean
  /** Set of levels the operator wants to see. Empty set = show all. */
  levels: ReadonlySet<LogLevel>
}

interface LogSearchProps {
  /** Total lines after applying the filter (for "<focused> of <total>"). */
  matchCount: number
  /** Currently-focused match index (1-based, 0 when no matches). */
  matchIndex: number
  /** Move to previous match (consumer wraps as needed). */
  onPrev: () => void
  /** Move to next match. */
  onNext: () => void
  /** Live filter — emitted whenever the operator changes the query,
   *  the regex toggle, or the level chips. */
  onFilterChange: (next: LogFilter) => void
  /** Optional initial filter (for deep-link restore). */
  initialFilter?: LogFilter
}

const EMPTY_LEVELS = new Set<LogLevel>()

export function LogSearch({
  matchCount,
  matchIndex,
  onPrev,
  onNext,
  onFilterChange,
  initialFilter,
}: LogSearchProps) {
  const [query, setQuery] = useState<string>(initialFilter?.query ?? '')
  const [regex, setRegex] = useState<boolean>(initialFilter?.regex ?? false)
  const [levels, setLevels] = useState<Set<LogLevel>>(
    initialFilter ? new Set(initialFilter.levels) : EMPTY_LEVELS,
  )

  const emit = useCallback(
    (next: Partial<LogFilter>) => {
      const merged: LogFilter = {
        query: next.query ?? query,
        regex: next.regex ?? regex,
        levels: next.levels ?? levels,
      }
      onFilterChange(merged)
    },
    [query, regex, levels, onFilterChange],
  )

  const onQueryChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const v = e.target.value
    setQuery(v)
    emit({ query: v })
  }
  const toggleRegex = () => {
    const next = !regex
    setRegex(next)
    emit({ regex: next })
  }
  const toggleLevel = (lvl: LogLevel) => {
    const next = new Set(levels)
    if (next.has(lvl)) next.delete(lvl)
    else next.add(lvl)
    setLevels(next)
    emit({ levels: next })
  }

  const counterLabel = useMemo(() => {
    if (matchCount === 0) return query.trim().length > 0 ? '0 of 0' : ''
    return `${matchIndex} of ${matchCount}`
  }, [matchCount, matchIndex, query])

  return (
    <div className="log-search" data-testid="log-search">
      <style>{LOG_SEARCH_CSS}</style>
      <div className="log-search-input-wrap">
        <svg className="log-search-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} aria-hidden>
          <circle cx="11" cy="11" r="7" />
          <path d="M21 21l-4.35-4.35" strokeLinecap="round" />
        </svg>
        <input
          type="search"
          className="log-search-input"
          placeholder={regex ? 'Search logs (regex)…' : 'Search logs…'}
          aria-label="Search log lines"
          data-testid="log-search-input"
          value={query}
          onChange={onQueryChange}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              if (e.shiftKey) onPrev()
              else onNext()
            }
          }}
        />
        {counterLabel ? (
          <span className="log-search-count" data-testid="log-search-count">
            {counterLabel}
          </span>
        ) : null}
        <button
          type="button"
          className="log-search-nav"
          aria-label="Previous match"
          data-testid="log-search-prev"
          onClick={onPrev}
          disabled={matchCount === 0}
        >
          ↑
        </button>
        <button
          type="button"
          className="log-search-nav"
          aria-label="Next match"
          data-testid="log-search-next"
          onClick={onNext}
          disabled={matchCount === 0}
        >
          ↓
        </button>
      </div>
      <div className="log-search-pills" role="group" aria-label="Log filters">
        <button
          type="button"
          className={`log-search-pill regex${regex ? ' active' : ''}`}
          aria-pressed={regex}
          data-testid="log-search-regex"
          onClick={toggleRegex}
          title="Toggle regex matching"
        >
          .*
        </button>
        {LOG_LEVELS.map((lvl) => {
          const active = levels.has(lvl)
          return (
            <button
              key={lvl}
              type="button"
              className={`log-search-pill level level-${lvl.toLowerCase()}${active ? ' active' : ''}`}
              aria-pressed={active}
              data-testid={`log-search-level-${lvl.toLowerCase()}`}
              onClick={() => toggleLevel(lvl)}
              title={`Show ${lvl} only (toggle)`}
            >
              {lvl}
            </button>
          )
        })}
      </div>
    </div>
  )
}

/** Test a log line's text against a filter. Pure helper so consumers
 *  can apply the same logic without re-deriving it inline. */
export function lineMatches(text: string, filter: LogFilter): boolean {
  const q = filter.query.trim()
  if (q.length === 0) return true
  if (filter.regex) {
    try {
      return new RegExp(q, 'i').test(text)
    } catch {
      // Invalid regex → fall through to substring so partially-typed
      // expressions don't make every line vanish mid-typing.
      return text.toLowerCase().includes(q.toLowerCase())
    }
  }
  return text.toLowerCase().includes(q.toLowerCase())
}

/* Issue #669 round 2 — LogSearch theme tokens.
 *
 * All hardcoded rgba/hex values routed through CSS variables so the
 * search bar reskins under [data-theme="light"] without losing AA
 * contrast. New tokens: --log-search-bg, --log-search-input-bg,
 * --log-search-input-text, --log-search-icon, --log-search-pill-text,
 * --log-search-pill-regex-{bg,fg,border}, plus per-level
 * --log-search-level-{info,warn,error,debug}-{bg,fg,border}. Light
 * peers are defined in globals.css. */
const LOG_SEARCH_CSS = `
.log-search {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  padding: 0.5rem 0.6rem;
  border-bottom: 1px solid var(--color-border);
  background: var(--log-search-bg, rgba(13,17,23,0.5));
  flex-shrink: 0;
}
.log-search-input-wrap {
  position: relative;
  display: flex;
  align-items: center;
  gap: 0.35rem;
}
.log-search-icon {
  position: absolute;
  left: 0.55rem;
  width: 13px;
  height: 13px;
  color: var(--log-search-icon, rgba(148,163,184,0.6));
  pointer-events: none;
}
.log-search-input {
  flex: 1 1 auto;
  padding: 0.34rem 0.55rem 0.34rem 1.7rem;
  background: var(--log-search-input-bg, rgba(13,17,23,0.85));
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--log-search-input-text, var(--color-text));
  font-size: 0.78rem;
  font-family: ui-monospace, SFMono-Regular, monospace;
  outline: none;
  transition: border-color 0.15s ease;
}
.log-search-input::placeholder {
  color: var(--log-search-input-placeholder, var(--color-text-dim));
}
.log-search-input:focus {
  border-color: var(--color-accent, #38BDF8);
}
.log-search-count {
  font-size: 0.7rem;
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  padding: 0 0.35rem;
}
.log-search-nav {
  appearance: none;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-dim);
  border-radius: 6px;
  width: 24px;
  height: 24px;
  font-size: 0.85rem;
  cursor: pointer;
  transition: background-color 0.12s ease, border-color 0.12s ease, color 0.12s ease;
  flex-shrink: 0;
}
.log-search-nav:hover:not(:disabled) {
  background: var(--log-search-nav-hover, rgba(148,163,184,0.1));
  color: var(--color-text-strong);
  border-color: var(--color-text-dim);
}
.log-search-nav:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.log-search-pills {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem;
}
.log-search-pill {
  appearance: none;
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  padding: 0.12rem 0.5rem;
  font-size: 0.62rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  cursor: pointer;
  color: var(--log-search-pill-text, var(--color-text-dim));
  transition: background-color 0.12s ease, color 0.12s ease, border-color 0.12s ease;
}
.log-search-pill:hover { color: var(--color-text-strong); }
.log-search-pill.regex {
  font-family: ui-monospace, monospace;
  text-transform: none;
  letter-spacing: 0;
}
.log-search-pill.regex.active {
  background: var(--log-search-pill-regex-bg, rgba(192,132,252,0.15));
  color: var(--log-search-pill-regex-fg, #C084FC);
  border-color: var(--log-search-pill-regex-border, rgba(192,132,252,0.45));
}
.log-search-pill.level-info.active   {
  background: var(--log-search-level-info-bg, rgba(56,139,253,0.18));
  color: var(--log-search-level-info-fg, #79b8ff);
  border-color: var(--log-search-level-info-border, rgba(56,139,253,0.45));
}
.log-search-pill.level-warn.active   {
  background: var(--log-search-level-warn-bg, rgba(245,158,11,0.18));
  color: var(--log-search-level-warn-fg, #f59e0b);
  border-color: var(--log-search-level-warn-border, rgba(245,158,11,0.45));
}
.log-search-pill.level-error.active  {
  background: var(--log-search-level-error-bg, rgba(248,81,73,0.18));
  color: var(--log-search-level-error-fg, #f85149);
  border-color: var(--log-search-level-error-border, rgba(248,81,73,0.45));
}
.log-search-pill.level-debug.active  {
  background: var(--log-search-level-debug-bg, rgba(148,163,184,0.18));
  color: var(--log-search-level-debug-fg, #94a3b8);
  border-color: var(--log-search-level-debug-border, rgba(148,163,184,0.45));
}
`
