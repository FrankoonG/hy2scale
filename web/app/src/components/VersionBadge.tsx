import { useTranslation } from 'react-i18next';
import clsx from 'clsx';

interface Props {
  version: string;
  /** This node: runs without the privileges some features need (red, "Limited"). */
  limited?: boolean;
  /** Compatibility mode (orange). This node: adds "Compat"; a peer: colour only. */
  compat?: boolean;
  /** A peer this node cannot talk to (red, ✗). */
  incompatible?: boolean;
  /** A peer older than the lossless minimum (↑). */
  outdated?: boolean;
  /** Badge of a peer row (symbols instead of words). */
  peer?: boolean;
  className?: string;
  title?: string;
}

/** "v1.4.0" pill, green / orange (compat) / red (limited, incompatible). */
export default function VersionBadge({ version, limited, compat, incompatible, outdated, peer, className, title }: Props) {
  const { t } = useTranslation();
  const suffix = peer
    ? (incompatible ? ' ✗' : outdated ? ' ↑' : '')
    : (limited ? ` ${t('version.limited')}` : compat ? ` ${t('version.compat')}` : '');
  const tone = limited ? 'limited' : incompatible ? 'incompatible' : compat ? 'compat' : undefined;
  return <span className={clsx('version-badge', tone, className)} title={title}>v{version}{suffix}</span>;
}
