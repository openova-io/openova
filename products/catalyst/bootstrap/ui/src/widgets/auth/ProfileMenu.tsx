/**
 * ProfileMenu — wizard-header avatar + sign-in/out menu (issue #689).
 *
 * Renders ONE of two states based on `useSession`:
 *
 *   • Anonymous:     [Sign in] button → opens PinSignInModal
 *   • Signed in:     <email-initial> circle → dropdown with email + Sign out
 *
 * The component is the canonical seam for both states — there is no
 * separate "anonymous header" vs "signed-in header"; the wizard layout
 * mounts ProfileMenu unconditionally and the component decides what to
 * paint based on the session.
 *
 * The menu uses a click-outside / ESC dismissal pattern so it behaves
 * the same on touch + desktop without needing the radix-ui dropdown
 * dependency. It is a small enough surface that the inline
 * implementation is preferable to pulling in another dropdown widget.
 */

import { useEffect, useRef, useState } from 'react'
import { useSession } from '@/shared/lib/useSession'
import { PinSignInModal } from './PinSignInModal'

export interface ProfileMenuProps {
  /** Optional pre-fill for the PIN modal's email field. */
  initialEmail?: string
  /** Optional class for the outer wrapper. */
  className?: string
}

export function ProfileMenu({ initialEmail = '', className }: ProfileMenuProps) {
  const session = useSession()
  const [modalOpen, setModalOpen] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement | null>(null)

  // Click-outside dismissal for the dropdown.
  useEffect(() => {
    if (!menuOpen) return
    function onClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false)
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setMenuOpen(false)
    }
    document.addEventListener('mousedown', onClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [menuOpen])

  // While the session is loading we render the anonymous chrome
  // pre-emptively — flashing a "signed in" state that then swaps to
  // anonymous would be worse UX than the inverse.
  if (!session.signedIn) {
    return (
      <>
        <button
          type="button"
          className={className}
          data-testid="profile-menu-signin"
          onClick={() => setModalOpen(true)}
          style={{
            background: 'transparent',
            border: '1px solid var(--wiz-border, rgba(255,255,255,0.12))',
            color: 'var(--wiz-text-md, #e2e8f0)',
            height: 30,
            padding: '0 12px',
            borderRadius: 7,
            cursor: 'pointer',
            font: 'inherit',
            fontSize: 12,
            fontWeight: 600,
          }}
        >
          Sign in
        </button>
        <PinSignInModal
          open={modalOpen}
          onClose={() => setModalOpen(false)}
          onAuthenticated={() => {
            setModalOpen(false)
            session.refetch()
          }}
          initialEmail={initialEmail}
        />
      </>
    )
  }

  const initial = session.email ? session.email[0]!.toUpperCase() : '?'

  return (
    <div ref={menuRef} className={className} style={{ position: 'relative' }}>
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={() => setMenuOpen((v) => !v)}
        data-testid="profile-menu-avatar"
        title={session.email ?? 'Signed in'}
        style={{
          width: 30,
          height: 30,
          borderRadius: '50%',
          background: 'rgba(56,189,248,0.15)',
          color: '#38BDF8',
          border: '1px solid rgba(56,189,248,0.3)',
          fontSize: 12,
          fontWeight: 700,
          cursor: 'pointer',
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: 0,
        }}
      >
        {initial}
      </button>
      {menuOpen && (
        <div
          role="menu"
          data-testid="profile-menu-dropdown"
          style={{
            position: 'absolute',
            top: 'calc(100% + 6px)',
            right: 0,
            minWidth: 220,
            background: 'var(--wiz-bg-input, #0f172a)',
            border: '1px solid var(--wiz-border, rgba(255,255,255,0.12))',
            borderRadius: 8,
            boxShadow: '0 12px 28px rgba(0,0,0,0.4)',
            padding: '8px 0',
            zIndex: 200,
          }}
        >
          <div
            style={{
              padding: '6px 12px',
              fontSize: 11,
              color: 'var(--wiz-text-sub, #94a3b8)',
              borderBottom: '1px solid var(--wiz-border-sub, rgba(255,255,255,0.06))',
              marginBottom: 6,
            }}
          >
            Signed in as
            <div
              style={{
                marginTop: 2,
                fontSize: 12,
                fontWeight: 600,
                color: 'var(--wiz-text-md, #e2e8f0)',
                wordBreak: 'break-all',
              }}
              data-testid="profile-menu-email"
            >
              {session.email}
            </div>
          </div>
          <button
            type="button"
            role="menuitem"
            data-testid="profile-menu-signout"
            onClick={() => {
              setMenuOpen(false)
              void session.signOut()
            }}
            style={{
              display: 'block',
              width: '100%',
              textAlign: 'left',
              padding: '6px 12px',
              background: 'transparent',
              border: 'none',
              color: 'var(--wiz-text-md, #e2e8f0)',
              fontSize: 12,
              cursor: 'pointer',
            }}
          >
            Sign out
          </button>
        </div>
      )}
    </div>
  )
}
