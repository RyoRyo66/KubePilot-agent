type Entry = { element: HTMLElement; close: () => void; dismissable: () => boolean; returnTo: HTMLElement | null }
const stack: Entry[] = []
let previousOverflow = ''
let shellWasInert = false
const focusable = 'button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

function controls(element: HTMLElement) {
  return Array.from(element.querySelectorAll<HTMLElement>(focusable)).filter(el => !el.closest('[inert]') && el.getClientRects().length > 0)
}
function focusFirst(element: HTMLElement) {
  const preferred = element.querySelector<HTMLElement>('[data-autofocus]')
  ;(preferred || controls(element)[0] || element).focus({ preventScroll: true })
}
function onKey(event: KeyboardEvent) {
  const top = stack.at(-1)
  if (!top) return
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopImmediatePropagation()
    if (top.dismissable()) top.close()
  }
  if (event.key === 'Tab') {
    const items = controls(top.element)
    const first = items[0]
    const last = items.at(-1)
    if (!first) { event.preventDefault(); top.element.focus(); return }
    if (event.shiftKey && (document.activeElement === first || document.activeElement === top.element)) {
      event.preventDefault(); last?.focus()
    } else if (!event.shiftKey && (document.activeElement === last || !top.element.contains(document.activeElement))) {
      event.preventDefault(); first.focus()
    }
  }
}
export function hasOpenDialog() { return stack.length > 0 }

export function registerDialog(element: HTMLElement, close: () => void, dismissable: () => boolean) {
  const shell = document.querySelector<HTMLElement>('.app-shell')
  const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null
  if (!stack.length) {
    previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    if (shell) { shellWasInert = shell.inert; shell.inert = true }
    document.addEventListener('keydown', onKey, true)
  }
  const previous = stack.at(-1)
  if (previous) { previous.element.inert = true; previous.element.setAttribute('aria-hidden', 'true') }
  const entry: Entry = { element, close, dismissable, returnTo }
  stack.push(entry)
  element.inert = false
  element.removeAttribute('aria-hidden')
  if (element.parentElement) element.parentElement.style.zIndex = String(100 + stack.length * 10)
  focusFirst(element)
  return () => {
    const index = stack.indexOf(entry)
    if (index < 0) return
    const wasTop = index === stack.length - 1
    stack.splice(index, 1)
    element.inert = true
    const top = stack.at(-1)
    if (top) { top.element.inert = false; top.element.removeAttribute('aria-hidden') }
    else {
      document.body.style.overflow = previousOverflow
      if (shell) shell.inert = shellWasInert
      document.removeEventListener('keydown', onKey, true)
    }
    if (wasTop) {
      if (returnTo?.isConnected && !returnTo.closest('[inert]')) returnTo.focus({ preventScroll: true })
      else if (top) focusFirst(top.element)
    }
  }
}
