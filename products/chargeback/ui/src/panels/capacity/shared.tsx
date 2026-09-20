import type { CapacityClassDef } from '../../api/types'
import { Badge } from '../../components/ui'
import { t } from '../../i18n'
import { classLabel, orderedClasses } from '../../lib/capacity'
import type { useAction } from '../../lib/useAction'

export type Act = ReturnType<typeof useAction>

/**
 * The label a class shows under. It comes from the CATALOGUE, keyed by the
 * class, so a second locale can name the three classes; the server's own
 * label is the fallback for a class this console does not know.
 */
export function classText(key: string, classes?: CapacityClassDef[] | null): string {
  switch (key) {
    case 'guaranteed':
      return t('capacity.class.guaranteed')
    case 'burstable':
      return t('capacity.class.burstable')
    case 'spot':
      return t('capacity.class.spot')
    default:
      return classLabel(key, classes)
  }
}

/** The badge tone of a class: guaranteed reads settled, burstable cautious, spot informational. */
export function classKind(key: string): 'ok' | 'warn' | 'info' {
  return key === 'guaranteed' ? 'ok' : key === 'spot' ? 'info' : 'warn'
}

export function ClassBadge({ cls, classes }: { cls: string; classes?: CapacityClassDef[] | null }) {
  return <Badge status={classText(cls, classes)} kind={classKind(cls)} />
}

/** The classes a pool enforces, as badges in canonical order. */
export function ClassBadges({ list, classes }: { list: ReadonlyArray<string>; classes?: CapacityClassDef[] | null }) {
  return (
    <span className="chips" data-classes={orderedClasses(list).join(',')}>
      {orderedClasses(list).map((c) => (
        <ClassBadge key={c} cls={c} classes={classes} />
      ))}
    </span>
  )
}
