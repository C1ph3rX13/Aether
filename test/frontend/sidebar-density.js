async page => {
  const origin = new URL(page.url()).origin
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const results = []
  for (const hasTouch of [false, true]) {
    const context = await page.context().browser().newContext({
      viewport: { width: 1440, height: 900 },
      hasTouch,
      reducedMotion: 'no-preference',
    })
    try {
      const current = await context.newPage()
      await current.goto(`${origin}/projects`)
      const navigation = current.getByRole('navigation', { name: 'Operations navigation', exact: true })
      await navigation.waitFor()
      await current.evaluate(() => document.fonts.ready)
      const measurements = await navigation.evaluate(element => {
        const box = selector => element.querySelector(selector).getBoundingClientRect()
        const style = selector => getComputedStyle(element.querySelector(selector))
        const workers = ['/workers/codex', '/workers/claude', '/workers/pi'].map(path => box(`a[href="${path}"]`))
        const action = [...element.querySelectorAll('button')].find(button => button.textContent === 'New Project')
        const canvas = document.createElement('canvas').getContext('2d')
        canvas.fillStyle = getComputedStyle(action).backgroundColor
        canvas.fillRect(0, 0, 1, 1)
        const container = element.closest('[data-slot="sidebar-container"]')
        return {
          coarse: matchMedia('(any-pointer: coarse)').matches,
          sidebarWidth: container.getBoundingClientRect().width,
          brandHeight: box('[aria-label="Aether home"]').height,
          action: action.getBoundingClientRect().toJSON(),
          workerHeights: workers.map(row => row.height),
          workerGaps: workers.slice(1).map((row, index) => row.top - workers[index].bottom),
          rowFont: style('[href="/workers/codex"]').fontSize,
          labelFont: style('[data-slot="sidebar-group-label"]').fontSize,
          family: style('[href="/workers/codex"]').fontFamily,
          iconWidth: box('[href="/workers/codex"] svg').width,
          primary: [...canvas.getImageData(0, 0, 1, 1).data].slice(0, 3),
          headerHeight: document.querySelector('header').getBoundingClientRect().height,
          transition: getComputedStyle(container).transitionDuration,
          easing: getComputedStyle(container).transitionTimingFunction,
          overflow: document.documentElement.scrollWidth > innerWidth,
        }
      })
      assert(measurements.coarse === hasTouch, 'The regression check exercises both pointer capabilities')
      assert(measurements.sidebarWidth === 288, 'dashboard-01 uses an 18rem Sidebar')
      assert(measurements.brandHeight === 32 && measurements.action.height === 32, 'Brand and primary action keep official 32px rows on touch and mouse')
      assert(measurements.action.x === 16 && measurements.action.y === 64, 'Primary action follows the official header and 16px horizontal inset')
      assert(measurements.workerHeights.every(height => height === 32) && measurements.workerGaps.every(gap => gap === 4), 'Pointer capability cannot inflate navigation density')
      assert(measurements.rowFont === '14px' && measurements.labelFont === '12px' && measurements.family.includes('Geist'), 'Official Geist navigation hierarchy')
      assert(measurements.iconWidth === 16 && measurements.primary.every(channel => channel === 0), 'Official icon dimensions and preview black primary')
      assert(measurements.headerHeight === 48 && measurements.transition === '0.2s' && measurements.easing === 'linear', 'Official 48px header and 200ms linear Sidebar motion')
      assert(!measurements.overflow, 'Desktop has no horizontal overflow')
      await current.screenshot({ path: `dist/frontend-catalog/sidebar-density/${hasTouch ? 'desktop-touch' : 'desktop'}.png`, fullPage: true, animations: 'disabled' })
      results.push({ hasTouch, ...measurements })

      if (hasTouch) {
        for (const width of [390, 320]) {
          await current.setViewportSize({ width, height: 844 })
          await current.getByRole('button', { name: 'Toggle Sidebar', exact: true }).click()
          const drawer = current.getByRole('dialog', { name: 'Operations navigation', exact: true })
          await drawer.waitFor()
          const rows = await drawer.locator('[data-slot="sidebar-menu-button"]').evaluateAll(elements => elements.map(element => ({ height: element.getBoundingClientRect().height, label: element.getAttribute('aria-label') || element.textContent })))
          assert(rows.every(row => row.height === (row.label === 'Aether account placeholder' ? 48 : 32)), 'Mobile Sheet keeps the official menu density')
          assert(!await current.evaluate(() => document.documentElement.scrollWidth > innerWidth), 'Mobile has no horizontal overflow')
          await current.screenshot({ path: `dist/frontend-catalog/sidebar-density/${width === 390 ? 'mobile' : 'mobile-320'}.png`, fullPage: true, animations: 'disabled' })
          await drawer.getByRole('button', { name: 'Close', exact: true }).click()
          await drawer.waitFor({ state: 'hidden' })
          assert(await current.getByRole('button', { name: 'Toggle Sidebar', exact: true }).evaluate(element => element === document.activeElement), 'Closing mobile navigation restores focus')
        }
      }
    } finally {
      await context.close()
    }
  }
  return { officialDensityAcrossPointers: 'passed', mobileNavigation: 'passed', measurements: results }
}
