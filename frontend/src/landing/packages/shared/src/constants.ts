export const COMPANY = {
  NAME: "Open Agents",
  SHORT_NAME: "Open Agents",
  MARKETING_URL: "https://orchestrator.inc",
  DOCS_URL: "https://orchestrator.inc/docs",
  GITHUB_URL: "https://github.com/sudo-adduser-jordan/open-agents",
  GITHUB_REPO: "sudo-adduser-jordan/open-agents",
  STATUS_URL: "https://status.aoagents.dev",
  TRUST_URL: "https://orchestrator.inc/privacy/",
  MAIL_TO: "mailto:prateek@untrivial.ai",
  // Social accounts are provisioned separately; do not publish guessed handles.
  X_URL: null as string | null,
  YOUTUBE_URL: "https://www.youtube.com/@itrytoohard",
  LINKEDIN_URL: null as string | null,
  DISCORD_URL: "https://discord.com/invite/UZv7JjxbwG",
  FOUNDERS_EMAIL: "prateek@untrivial.ai",
  REPORT_ISSUE_URL: "https://github.com/sudo-adduser-jordan/open-agents/issues/new",
  LICENSE: "Apache-2.0",
  LICENSE_URL: "https://github.com/sudo-adduser-jordan/open-agents/blob/main/LICENSE",
} as const;

export const THEME_STORAGE_KEY = "open-agents-theme";
export const POSTHOG_COOKIE_NAME = "ph_phc_";

export const OPEN_ROLES = [] as { title: string; url: string; location: string }[];

export const PLATFORMS = {
  MACOS: "macos",
  WINDOWS: "windows",
  LINUX: "linux",
} as const;

export const GITHUB_STARS_URL = "https://api.github.com/repos/sudo-adduser-jordan/open-agents";

// macOS points at the .dmg: this is rollout step 6 of issue #3267, taken once the
// release conductor started publishing a signed, notarized dmg on the stable
// channel. Mounting it gives the drag-to-Applications window, so the app lands in
// /Applications instead of being unzipped into ~/Downloads and launched from
// there, which is what leaves macOS running it translocated or as a stale copy
// (#3617, #3527).
//
// The .zip keeps publishing forever regardless: MacUpdater can only install an
// update from a zip (findFile(files, "zip", ["pkg", "dmg"])), so the dmg is
// first-install only and never replaces it.
//
// These are static releases/latest/download links, so they 404 until a release
// actually carries the asset. Only the STABLE channel builds a dmg; if the links
// ever break, check that the newest non-prerelease release has both files rather
// than assuming the pipeline is broken. The download page itself is resilient
// here: it reads the live release list and falls back to the zip.
export const DOWNLOAD_URL_MAC_ARM64 = "https://github.com/sudo-adduser-jordan/open-agents/releases/latest/download/open-agents-darwin-arm64.dmg";
export const DOWNLOAD_URL_MAC_X64 = "https://github.com/sudo-adduser-jordan/open-agents/releases/latest/download/open-agents-darwin-x64.dmg";
export const DOWNLOAD_URL_WINDOWS = "https://github.com/sudo-adduser-jordan/open-agents/releases/latest/download/open-agents-win32-x64.exe";
export const DOWNLOAD_URL_LINUX = "https://github.com/sudo-adduser-jordan/open-agents/releases/latest/download/open-agents-linux-x64.AppImage";

export const AGENT_HARNESSES = 24;
export const TAGLINE = "Stop babysitting agents. Start merging real work.";
export const HERO_SUBHEADLINE = "Run a fleet of coding agents while keeping branches, reviews, and CI failures manageable.";
export const HERO_SECONDARY_SUBHEADLINE = "Isolated workspaces for OpenCode, OpenCode, and any CLI agent. Review every change from one dashboard. Free and open source.";

export const NAV_ITEMS = [
  { label: "Demo", href: "/#see-it" },
  { label: "Features", href: "/#features" },
  { label: "Changelog", href: "/changelog" },
  { label: "Docs", href: "/docs" },
] as const;
