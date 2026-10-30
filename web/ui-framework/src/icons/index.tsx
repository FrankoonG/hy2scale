/**
 * Icon set: one 24×24 grid, stroke-only, `currentColor`, round caps/joins.
 *
 * Paths follow Lucide (https://lucide.dev), ISC License:
 *   Copyright (c) for portions of Lucide are held by Cole Bemis 2013-2022 as
 *   part of Feather (MIT). All other copyright (c) for Lucide are held by
 *   Lucide Contributors 2022.
 *   Permission to use, copy, modify, and/or distribute this software for any
 *   purpose with or without fee is hereby granted, provided that the above
 *   copyright notice and this permission notice appear in all copies.
 *
 * Every icon is a named component (`<IconCheck size={14} />`). Colour comes
 * from `color` / `currentColor` only, which is also what Dark Reader handles
 * best (docs/dark-reader-testing.md). Without `title` an icon is decorative
 * (`aria-hidden`); clickable icons go through `IconButton label=…`.
 */
import type { ComponentType, ReactNode, SVGProps } from 'react';
import clsx from 'clsx';

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, 'ref' | 'children'> {
  /** Width and height in px (default 16). CSS may still size it. */
  size?: number | string;
  strokeWidth?: number | string;
  /** Accessible name; omit for decorative icons. */
  title?: string;
}

export type IconComponent = ComponentType<IconProps>;

function icon(name: string, body: ReactNode): IconComponent {
  const C = ({ size = 16, strokeWidth = 2, title, className, ...rest }: IconProps) => (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={clsx('hy-icon', className)}
      data-icon={name}
      aria-hidden={title ? undefined : true}
      role={title ? 'img' : undefined}
      focusable="false"
      {...rest}
    >
      {title && <title>{title}</title>}
      {body}
    </svg>
  );
  C.displayName = `Icon(${name})`;
  return C;
}

// ---- navigation ----
export const IconLayoutDashboard = icon('layout-dashboard', <>
  <rect width="7" height="9" x="3" y="3" rx="1" /><rect width="7" height="5" x="14" y="3" rx="1" />
  <rect width="7" height="9" x="14" y="12" rx="1" /><rect width="7" height="5" x="3" y="16" rx="1" />
</>);
export const IconWaypoints = icon('waypoints', <>
  <circle cx="12" cy="4.5" r="2.5" /><path d="m10.2 6.3-3.9 3.9" /><circle cx="4.5" cy="12" r="2.5" />
  <path d="M7 12h10" /><circle cx="19.5" cy="12" r="2.5" /><path d="m13.8 17.7 3.9-3.9" />
  <circle cx="12" cy="19.5" r="2.5" />
</>);
export const IconEthernetPort = icon('ethernet-port', <>
  <path d="m15 20 3-3h2a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h2l3 3z" />
  <path d="M6 8v1" /><path d="M10 8v1" /><path d="M14 8v1" /><path d="M18 8v1" />
</>);
export const IconSplit = icon('split', <>
  <path d="M16 3h5v5" /><path d="M8 3H3v5" /><path d="M12 22v-8.3a4 4 0 0 0-1.172-2.872L3 3" /><path d="m15 9 6-6" />
</>);
export const IconUsers = icon('users', <>
  <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" />
  <path d="M22 21v-2a4 4 0 0 0-3-3.87" /><path d="M16 3.13a4 4 0 0 1 0 7.75" />
</>);
export const IconServer = icon('server', <>
  <rect width="20" height="8" x="2" y="2" rx="2" /><rect width="20" height="8" x="2" y="14" rx="2" />
  <path d="M6 6h.01" /><path d="M6 18h.01" />
</>);
export const IconPlug = icon('plug', <>
  <path d="M12 22v-5" /><path d="M9 8V2" /><path d="M15 8V2" /><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z" />
</>);
export const IconShieldCheck = icon('shield-check', <>
  <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10" /><path d="m9 12 2 2 4-4" />
</>);
export const IconScrollText = icon('scroll-text', <>
  <path d="M15 12h-5" /><path d="M15 8h-5" /><path d="M19 17V5a2 2 0 0 0-2-2H4" />
  <path d="M8 21h12a2 2 0 0 0 2-2v-1a1 1 0 0 0-1-1H11a1 1 0 0 0-1 1v1a2 2 0 1 1-4 0V5a2 2 0 1 0-4 0v2a1 1 0 0 0 1 1h3" />
</>);
export const IconSettings = icon('settings', <>
  <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
  <circle cx="12" cy="12" r="3" />
</>);
export const IconList = icon('list', <>
  <path d="M3 6h.01" /><path d="M3 12h.01" /><path d="M3 18h.01" /><path d="M8 6h13" /><path d="M8 12h13" /><path d="M8 18h13" />
</>);
export const IconNetwork = icon('network', <>
  <rect x="16" y="16" width="6" height="6" rx="1" /><rect x="2" y="16" width="6" height="6" rx="1" />
  <rect x="9" y="2" width="6" height="6" rx="1" /><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3" /><path d="M12 12V8" />
</>);
export const IconPanelLeftClose = icon('panel-left-close', <>
  <rect width="18" height="18" x="3" y="3" rx="2" /><path d="M9 3v18" /><path d="m16 15-3-3 3-3" />
</>);
export const IconPanelLeftOpen = icon('panel-left-open', <>
  <rect width="18" height="18" x="3" y="3" rx="2" /><path d="M9 3v18" /><path d="m14 9 3 3-3 3" />
</>);
export const IconMenu = icon('menu', <>
  <path d="M4 6h16" /><path d="M4 12h16" /><path d="M4 18h16" />
</>);

// ---- account / preferences ----
export const IconCircleUser = icon('circle-user', <>
  <circle cx="12" cy="12" r="10" /><circle cx="12" cy="10" r="3" /><path d="M7 20.662V19a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v1.662" />
</>);
export const IconLanguages = icon('languages', <>
  <path d="m5 8 6 6" /><path d="m4 14 6-6 2-3" /><path d="M2 5h12" /><path d="M7 2h1" />
  <path d="m22 22-5-10-5 10" /><path d="M14 18h6" />
</>);
export const IconSunMoon = icon('sun-moon', <>
  <path d="M12 8a2.83 2.83 0 0 0 4 4 4 4 0 1 1-4-4" /><path d="M12 2v2" /><path d="M12 20v2" />
  <path d="m4.9 4.9 1.4 1.4" /><path d="m17.7 17.7 1.4 1.4" /><path d="M2 12h2" /><path d="M20 12h2" />
  <path d="m6.3 17.7-1.4 1.4" /><path d="m19.1 4.9-1.4 1.4" />
</>);
export const IconSun = icon('sun', <>
  <circle cx="12" cy="12" r="4" /><path d="M12 2v2" /><path d="M12 20v2" /><path d="m4.93 4.93 1.41 1.41" />
  <path d="m17.66 17.66 1.41 1.41" /><path d="M2 12h2" /><path d="M20 12h2" /><path d="m6.34 17.66-1.41 1.41" />
  <path d="m19.07 4.93-1.41 1.41" />
</>);
export const IconMoon = icon('moon', <path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z" />);
export const IconLogOut = icon('log-out', <>
  <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" /><path d="m16 17 5-5-5-5" /><path d="M21 12H9" />
</>);
/** Disconnect (kick) a client session. */
export const IconUnplug = icon('unplug', <>
  <path d="m19 5 3-3" /><path d="m2 22 3-3" /><path d="M6.3 20.3a2.4 2.4 0 0 0 3.4 0L12 18l-6-6-2.3 2.3a2.4 2.4 0 0 0 0 3.4Z" />
  <path d="M7.5 13.5 10 11" /><path d="M10.5 16.5 13 14" /><path d="m12 6 6 6 2.3-2.3a2.4 2.4 0 0 0 0-3.4l-2.6-2.6a2.4 2.4 0 0 0-3.4 0Z" />
</>);
export const IconGlobe = icon('globe', <>
  <circle cx="12" cy="12" r="10" /><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20" /><path d="M2 12h20" />
</>);

// ---- chevrons / marks ----
export const IconChevronDown = icon('chevron-down', <path d="m6 9 6 6 6-6" />);
export const IconChevronRight = icon('chevron-right', <path d="m9 18 6-6-6-6" />);
export const IconChevronLeft = icon('chevron-left', <path d="m15 18-6-6 6-6" />);
export const IconChevronUp = icon('chevron-up', <path d="m18 15-6-6-6 6" />);
export const IconChevronsUpDown = icon('chevrons-up-down', <><path d="m7 15 5 5 5-5" /><path d="m7 9 5-5 5 5" /></>);
export const IconCheck = icon('check', <path d="M20 6 9 17l-5-5" />);
export const IconX = icon('x', <><path d="M18 6 6 18" /><path d="m6 6 12 12" /></>);
export const IconPlus = icon('plus', <><path d="M5 12h14" /><path d="M12 5v14" /></>);
export const IconLink = icon('link', <><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" /></>);
export const IconMinus = icon('minus', <path d="M5 12h14" />);
export const IconDownload = icon('download', <><path d="M12 15V3" /><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><path d="m7 10 5 5 5-5" /></>);
export const IconUpload = icon('upload', <><path d="M12 3v12" /><path d="m17 8-5-5-5 5" /><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /></>);
export const IconCircleAlert = icon('circle-alert', <>
  <circle cx="12" cy="12" r="10" /><path d="M12 8v4" /><path d="M12 16h.01" />
</>);

// ---- actions ----
/** Overflow: every action of a toolbar that does not fit. */
export const IconEllipsisVertical = icon('ellipsis-vertical', <>
  <circle cx="12" cy="12" r="1" /><circle cx="12" cy="5" r="1" /><circle cx="12" cy="19" r="1" />
</>);
/** View options (display toggles), as opposed to actions. */
export const IconSlidersHorizontal = icon('sliders-horizontal', <>
  <path d="M21 4h-7" /><path d="M10 4H3" /><path d="M21 12h-9" /><path d="M8 12H3" /><path d="M21 20h-5" /><path d="M12 20H3" />
  <path d="M14 2v4" /><path d="M8 10v4" /><path d="M16 18v4" />
</>);
/** Generate a value (password, key pair, certificate). */
export const IconWandSparkles = icon('wand-sparkles', <>
  <path d="m21.64 3.64-1.28-1.28a1.21 1.21 0 0 0-1.72 0L2.36 18.64a1.21 1.21 0 0 0 0 1.72l1.28 1.28a1.2 1.2 0 0 0 1.72 0L21.64 5.36a1.2 1.2 0 0 0 0-1.72" />
  <path d="m14 7 3 3" /><path d="M5 6v4" /><path d="M19 14v4" /><path d="M10 2v2" /><path d="M7 8H3" /><path d="M21 16h-4" /><path d="M11 3H9" />
</>);
export const IconEye = icon('eye', <>
  <path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z" /><circle cx="12" cy="12" r="3" />
</>);
export const IconEyeOff = icon('eye-off', <>
  <path d="M9.88 9.88a3 3 0 1 0 4.24 4.24" /><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68" />
  <path d="M6.61 6.61A13.526 13.526 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61" /><path d="m2 2 20 20" />
</>);
export const IconColumns3 = icon('columns-3', <>
  <rect width="18" height="18" x="3" y="3" rx="2" /><path d="M9 3v18" /><path d="M15 3v18" />
</>);
export const IconGripVertical = icon('grip-vertical', <>
  <circle cx="9" cy="12" r="1" /><circle cx="9" cy="5" r="1" /><circle cx="9" cy="19" r="1" />
  <circle cx="15" cy="12" r="1" /><circle cx="15" cy="5" r="1" /><circle cx="15" cy="19" r="1" />
</>);
export const IconCopy = icon('copy', <>
  <rect width="14" height="14" x="8" y="8" rx="2" /><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />
</>);
/** Open the full details of an item. */
export const IconMaximize2 = icon('maximize-2', <>
  <path d="M15 3h6v6" /><path d="M9 21H3v-6" /><path d="M21 3l-7 7" /><path d="M3 21l7-7" />
</>);

// ---- canvas tools ----
export const IconScan = icon('scan', <>
  <path d="M3 7V5a2 2 0 0 1 2-2h2" /><path d="M17 3h2a2 2 0 0 1 2 2v2" /><path d="M21 17v2a2 2 0 0 1-2 2h-2" /><path d="M7 21H5a2 2 0 0 1-2-2v-2" />
</>);
export const IconZoomIn = icon('zoom-in', <>
  <circle cx="11" cy="11" r="8" /><path d="m21 21-4.35-4.35" /><path d="M11 8v6" /><path d="M8 11h6" />
</>);
export const IconZoomOut = icon('zoom-out', <>
  <circle cx="11" cy="11" r="8" /><path d="m21 21-4.35-4.35" /><path d="M8 11h6" />
</>);
export const IconLayoutGrid = icon('layout-grid', <>
  <rect width="7" height="7" x="3" y="3" rx="1" /><rect width="7" height="7" x="14" y="3" rx="1" />
  <rect width="7" height="7" x="14" y="14" rx="1" /><rect width="7" height="7" x="3" y="14" rx="1" />
</>);
export const IconMagnet = icon('magnet', <>
  <path d="m6 15-4-4 6.75-6.77a7.79 7.79 0 0 1 11 11L13 22l-4-4 6.39-6.36a2.14 2.14 0 0 0-3-3L6 15" /><path d="m5 8 4 4" /><path d="m12 15 4 4" />
</>);
export const IconLayers = icon('layers', <>
  <path d="m12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z" />
  <path d="m22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65" /><path d="m22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65" />
</>);
