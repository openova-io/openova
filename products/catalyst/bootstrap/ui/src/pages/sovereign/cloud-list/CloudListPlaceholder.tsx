/**
 * CloudListPlaceholder — empty surface for resource list pages whose
 * data is not yet fed by an informer (Services / Ingresses /
 * DNS Zones / Storage Classes — see issue #321 for the informer rollout).
 *
 * Renders the canonical list-page header so the route exists and is
 * navigable, then a clear empty state explaining the gap with a link
 * back to the documentation tracking issue.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target shape) — the
 * page surface lands in target shape now; the data wiring fills in
 * later without changing the URL or component contract.
 */

import { useCloud } from '../CloudPage'
import {
  CloudListHeader,
  EmptyState,
} from './cloudListShared'
import { CLOUD_LIST_CSS } from './cloudListCss'

interface CloudListPlaceholderProps {
  /** Per-page testid prefix (e.g. "cloud-services"). */
  testId: string
  /** Plural resource title (e.g. "Services"). */
  title: string
  /** Tagline beneath the title. */
  tagline: string
  /** Empty-state body — usually references the informer issue. */
  bodyText: string
  /** Optional documentation URL (rendered as a link). */
  docsHref?: string
}

export function CloudListPlaceholder({
  testId,
  title,
  tagline,
  bodyText,
  docsHref,
}: CloudListPlaceholderProps) {
  const { deploymentId } = useCloud()
  return (
    <div data-testid={`${testId}-page`}>
      <style>{CLOUD_LIST_CSS}</style>
      <CloudListHeader
        testId={testId}
        title={title}
        tagline={tagline}
        count={0}
        deploymentId={deploymentId}
      />
      <EmptyState
        testId={`${testId}-empty`}
        title={`No ${title.toLowerCase()} yet.`}
        body={
          <>
            {bodyText}
            {docsHref ? (
              <>
                {' '}
                <a
                  href={docsHref}
                  target="_blank"
                  rel="noreferrer"
                  className="text-[var(--color-accent)] underline"
                  data-testid={`${testId}-docs-link`}
                >
                  Tracking issue ↗
                </a>
              </>
            ) : null}
          </>
        }
      />
    </div>
  )
}
