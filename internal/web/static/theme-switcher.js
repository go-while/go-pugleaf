/*!
 * Color mode toggler for go-pugleaf
 * Based on Bootstrap's official color mode implementation
 * Supports light, dark, and auto modes
 */

(() => {
  'use strict'

  const getStoredTheme = () => localStorage.getItem('theme')
  const setStoredTheme = theme => localStorage.setItem('theme', theme)

  const getPreferredTheme = () => {
    const storedTheme = getStoredTheme()
    if (storedTheme) {
      return storedTheme
    }
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  }

  const setTheme = theme => {
    if (theme === 'auto') {
      document.documentElement.setAttribute('data-bs-theme',
        (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'))
    } else {
      document.documentElement.setAttribute('data-bs-theme', theme)
    }
  }

  // Set theme immediately to prevent flash
  setTheme(getPreferredTheme())

  const showActiveTheme = (theme, focus = false) => {
    const themeSwitcher = document.querySelector('#bd-theme')

    if (!themeSwitcher) {
      return
    }

    const themeSwitcherText = document.querySelector('#bd-theme-text')
    const activeThemeIcon = document.querySelector('.theme-icon-active')
    const btnToActive = document.querySelector(`[data-bs-theme-value="${theme}"]`)

    if (!btnToActive) return

    document.querySelectorAll('[data-bs-theme-value]').forEach(element => {
      element.classList.remove('active')
      element.setAttribute('aria-pressed', 'false')
    })

    btnToActive.classList.add('active')
    btnToActive.setAttribute('aria-pressed', 'true')

    if (activeThemeIcon) {
      const icon = btnToActive.querySelector('i')
      if (icon) {
        activeThemeIcon.className = icon.className
      }
    }

    if (themeSwitcherText) {
      themeSwitcherText.textContent = btnToActive.textContent.trim()
    }

    if (focus) {
      themeSwitcher.focus()
    }
  }

  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    const storedTheme = getStoredTheme()
    if (storedTheme !== 'light' && storedTheme !== 'dark') {
      setTheme(getPreferredTheme())
    }
  })

  window.addEventListener('DOMContentLoaded', () => {
    showActiveTheme(getPreferredTheme())

    document.querySelectorAll('[data-bs-theme-value]')
      .forEach(toggle => {
        toggle.addEventListener('click', (e) => {
          e.preventDefault()
          const theme = toggle.getAttribute('data-bs-theme-value')
          setStoredTheme(theme)
          setTheme(theme)
          showActiveTheme(theme, true)
        })
      })
  })
})()

// Legacy theme support for existing theme selector
function changeTheme() {
  const themeSelect = document.getElementById('theme-select')
  if (!themeSelect) return

  const theme = themeSelect.value

  // Map legacy themes to new system
  const themeMap = {
    'bootstrap': 'light',
    'modern': 'light',
    'modern-dark': 'dark',
    'classic': 'light'
  }

  const newTheme = themeMap[theme] || 'light'

  // Store theme preferences
  localStorage.setItem('preferred-theme', theme)
  localStorage.setItem('theme', newTheme)

  // Apply Bootstrap theme
  document.documentElement.setAttribute('data-bs-theme', newTheme)

  // Also apply legacy theme CSS for custom styling
  const existingLink = document.querySelector('link[href*="theme-"]')
  if (existingLink) {
    existingLink.href = `/static/theme-${theme}.css`
  } else {
    const link = document.createElement('link')
    link.rel = 'stylesheet'
    link.href = `/static/theme-${theme}.css`
    document.head.appendChild(link)
  }

  // Refresh the page to ensure all styles are properly applied
  setTimeout(() => {
    window.location.reload()
  }, 100) // Small delay to ensure localStorage is saved
}
