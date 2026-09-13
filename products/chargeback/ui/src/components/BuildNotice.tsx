import { useSyncExternalStore } from 'react'
import { t } from '../i18n'
import { buildSnapshot, buildsDiffer, pageBuild, serverBuild, subscribeBuild } from '../lib/build'

/**
 * The notice a page shows once it has been left open across a deploy.
 *
 * WHAT IT IS NOT, and why. Not a modal: a modal would land on top of whatever
 * the user is halfway through typing, and the pool that prompted this whole
 * change takes a name, a machine count and a per-machine vector to fill in.
 * Not a toast: a toast that fades is a notice nobody read. Not an automatic
 * reload: losing a half-typed pool is worse than reading a banner. So it is a
 * strip that sits at the top of the console, stays until the page is actually
 * reloaded, and leaves every control underneath it working — the user can
 * finish the form, and reload when they are ready to.
 *
 * It renders nothing at all unless BOTH builds are known and they differ
 * (lib/build.ts), so a dev server, an unstamped shell or a binary with no
 * version never produces it.
 */
export function BuildNotice() {
  useSyncExternalStore(subscribeBuild, buildSnapshot, buildSnapshot)
  const page = pageBuild()
  const server = serverBuild()
  if (!buildsDiffer(page, server)) return null
  return (
    <div className="banner build" role="status" data-stale-build={`${page}→${server}`}>
      <span>
        <b>{t('build.stale.title')}</b> {t('build.stale.body', { page, server })}
      </span>
      <button className="small primary" onClick={() => window.location.reload()}>
        {t('build.stale.reload')}
      </button>
    </div>
  )
}
