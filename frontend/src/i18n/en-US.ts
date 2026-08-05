// English (US) language pack.
// Typed against `Messages` (derived from zh-CN.ts), so a missing or extra key here
// is a COMPILE ERROR — key-structure parity with zh-CN is enforced mechanically.

import type { Messages } from './zh-CN'

export const enUS: Messages = {
  app: {
    title: 'Lumo AI',
    tagline: 'A local-first smart quiz & learning platform',
  },
  nav: {
    dashboard: 'Dashboard',
    library: 'Library',
    settings: 'Settings',
  },
  common: {
    save: 'Save',
    cancel: 'Cancel',
    confirm: 'Confirm',
    delete: 'Delete',
    back: 'Back',
    retry: 'Retry',
    language: 'Language',
    greeting: 'Hello, {name}!',
  },
}
