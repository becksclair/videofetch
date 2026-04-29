type SidebarActionAPI = {
  open?: () => Promise<void> | void;
};

type BrowserAPI = typeof chrome & {
  sidebarAction?: SidebarActionAPI;
};

const globalWebExt = globalThis as typeof globalThis & {
  browser?: BrowserAPI;
  chrome?: BrowserAPI;
};

const resolvedExt = globalWebExt.browser ?? globalWebExt.chrome;
if (!resolvedExt) {
  throw new Error('webext_api_unavailable');
}

export const ext = resolvedExt;

export function originPermissionForBaseUrl(baseUrl: string): string {
  const parsed = new URL(baseUrl);
  return `${parsed.protocol}//${parsed.hostname}/*`;
}

export async function hasServerPermission(baseUrl: string): Promise<boolean> {
  const permissions = ext.permissions;
  if (!permissions) {
    return true;
  }
  return permissions.contains({ origins: [originPermissionForBaseUrl(baseUrl)] });
}

export async function openPrimaryPanel(): Promise<void> {
  const sidePanel = ext.sidePanel as { open?: (options: { windowId: number }) => Promise<void> | void } | undefined;
  if (sidePanel?.open) {
    await sidePanel.open({ windowId: ext.windows.WINDOW_ID_CURRENT });
    return;
  }

  if (ext.sidebarAction?.open) {
    await ext.sidebarAction.open();
    return;
  }

  throw new Error('panel_unavailable');
}
