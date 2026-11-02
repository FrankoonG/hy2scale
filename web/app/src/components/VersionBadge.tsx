import { useTranslation } from 'react-i18next';
import clsx from 'clsx';

interface Props {
  version: string;
  /** This node: runs without the privileges some features need (red, "Limited"). */
  limited?: boolean;
  /** A peer in compatibility mode (orange). */
  compat?: boolean;
  /** This node: a newer release is available (yellow). */
  update?: boolean;
  /** A peer this node cannot talk to (red, ✗). */
  incompatible?: boolean;
  /** A peer older than the lossless minimum (↑). */
  outdated?: boolean;
  /** Badge of a peer row (symbols instead of words). */
  peer?: boolean;
  /** This node: not an official (signed) release build ("*"). */
  selfBuilt?: boolean;
  className?: string;
  title?: string;
}

/** "v1.4.0" pill, green / yellow (update) / orange (compat peer) / red (limited, incompatible). */
export default function VersionBadge({ version, limited, compat, update, incompatible, outdated, peer, selfBuilt, className, title }: Props) {
  const { t } = useTranslation();
  const suffix = peer
    ? (incompatible ? ' ✗' : outdated ? ' ↑' : '')
    : (limited ? ` ${t('version.limited')}` : '');
  const tone = limited ? 'limited' : incompatible ? 'incompatible' : update ? 'update' : (peer && compat) ? 'compat' : undefined;
  return (
    <span className={clsx('version-badge', tone, className)} title={title ?? (selfBuilt ? t('build.selfBuilt') : undefined)} data-self-built={selfBuilt || undefined}>
      v{version}{selfBuilt ? '*' : ''}{suffix}
    </span>
  );
}
