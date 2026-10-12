async page => {
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const origin = new URL(page.url()).origin
  const api = async (path, data, method = 'post') => {
    const response = await page.request[method](`${origin}/api/v1${path}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${path}: ${response.status()} ${await response.text()}`)
    return response.json()
  }
  const refresh = async () => {
    const read = page.waitForResponse(response => response.url().endsWith('/api/v1/projects') && response.ok())
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await read
  }
  const created = []
  let touchContext
  try {
    await page.goto(`${origin}/projects`)
    for (let index = 1; index <= 12; index++) {
      const detail = await api('/projects', {
        title: `Reflow Project ${String(index).padStart(2, '0')}${index === 1 ? ' ' + 'LongTitle'.repeat(16) : ''}`,
        origin: 'Isolated responsive acceptance evidence', goal: 'Verify Catalog reflow', bootstrap_enabled: false,
      })
      created.push(detail.project)
    }
    await api(`/projects/${created[0].id}/actions/pause`, { expected_revision: created[0].revision })
    await page.setViewportSize({ width: 390, height: 844 })
    await refresh()
    await page.getByLabel('Search Projects', { exact: true }).fill('Reflow Project')
    await page.getByText('12 of 21 Projects', { exact: true }).waitFor()
    const firstTitle = created[0].title
    const projectHeader = page.getByRole('columnheader', { name: 'Sort by Project', exact: true })
    for (let attempt = 0; attempt < 3 && await projectHeader.getAttribute('aria-sort') !== 'ascending'; attempt++) {
      await page.getByRole('button', { name: 'Sort by Project', exact: true }).click()
    }
    await page.getByRole('checkbox', { name: `Select ${firstTitle}`, exact: true }).check()
    await page.getByRole('button', { name: 'Go to next page', exact: true }).click()
    await page.getByText('Page 2 of 2', { exact: true }).waitFor()
    await page.setViewportSize({ width: 1440, height: 900 })
    assert(await page.getByText('1 selected', { exact: true }).isVisible(), 'Width changes preserve selection')
    assert(await page.getByText('Page 2 of 2', { exact: true }).isVisible(), 'Width changes preserve pagination')
    await page.setViewportSize({ width: 320, height: 568 })
    await page.getByRole('checkbox', { name: 'Select all visible Projects', exact: true }).check()
    await page.getByText('12 selected', { exact: true }).waitFor()
    await page.getByLabel('Lifecycle', { exact: true }).click()
    await page.getByRole('option', { name: 'Paused', exact: true }).click()
    await page.getByText('12 selected · 11 hidden by filters', { exact: true }).waitFor()
    const row = page.getByRole('row', { name: `Open ${firstTitle}`, exact: true })
    await row.waitFor()
    assert(await row.locator('td').count() === 8, 'Reflow retains the original cells')
    assert(await row.evaluate(element => element.scrollWidth <= element.clientWidth), 'Long titles wrap within the record')
    assert(await row.evaluate(element => {
      const cells = element.querySelectorAll('td')
      return cells[1].getBoundingClientRect().bottom <= cells[2].getBoundingClientRect().top
    }), 'Wrapping titles do not overlap the next field')
    await page.screenshot({ path: 'dist/frontend-catalog/catalog-reflow-long-title-320.png', fullPage: true })
    assert(await page.getByRole('button', { name: `Actions for ${firstTitle}`, exact: true }).count() === 1, 'Only one action trigger is mounted')
    assert(await page.locator(`[id="project-actions-${created[0].id}"]`).count() === 1, 'Action IDs remain unique')
    await page.getByRole('button', { name: `Actions for ${firstTitle}`, exact: true }).click()
    await page.getByRole('menuitem', { name: 'Resume', exact: true }).click()
    const confirmation = page.getByRole('alertdialog', { name: 'Resume Project', exact: true })
    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.waitForFunction(title => document.activeElement?.getAttribute('aria-label') === `Actions for ${title}`, firstTitle)
    await page.keyboard.press('Enter')
    await page.getByRole('menuitem', { name: 'Resume', exact: true }).click()
    await confirmation.getByRole('button', { name: 'Resume', exact: true }).click()
    await confirmation.waitFor({ state: 'hidden' })
    assert((await api(`/projects/${created[0].id}`, undefined, 'get')).project.status === 'active', 'Narrow record commands persist authoritative state')
    await page.getByRole('button', { name: 'Clear selection', exact: true }).click()
    await page.getByLabel('Search Projects', { exact: true }).fill('')
    await page.getByLabel('Lifecycle', { exact: true }).click()
    await page.getByRole('option', { name: 'All lifecycles', exact: true }).click()

    touchContext = await page.context().browser().newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, locale: 'zh-CN' })
    const touch = await touchContext.newPage()
    await touch.goto(`${origin}/projects`)
    await touch.getByLabel('Search Projects', { exact: true }).fill(firstTitle)
    const touchRow = touch.getByRole('row', { name: `Open ${firstTitle}`, exact: true })
    await touchRow.getByRole('checkbox', { name: `Select ${firstTitle}`, exact: true }).tap()
    await touch.getByText('1 selected', { exact: true }).waitFor()
    await touch.getByRole('button', { name: `Actions for ${firstTitle}`, exact: true }).tap()
    await touch.getByRole('menuitem', { name: 'Pause', exact: true }).tap()
    const pause = touch.getByRole('alertdialog', { name: 'Pause Project', exact: true })
    await pause.getByRole('button', { name: 'Cancel', exact: true }).tap()
    const before = touch.url()
    await touchRow.getByText('Actions', { exact: true }).tap()
    assert(touch.url() === before, 'An action label does not navigate to the Project')
    const scrollBefore = await touch.evaluate(() => window.scrollY)
    const direction = await touch.evaluate(() => window.scrollY + innerHeight + 1 < document.documentElement.scrollHeight ? -1 : 1)
    const cdp = await touchContext.newCDPSession(touch)
    const bounds = await touchRow.boundingBox()
    const x = bounds.x + bounds.width / 2
    const y = Math.min(bounds.y + 200, 730)
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] })
    for (const offset of [30, 60, 90, 120, 150]) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: y + direction * offset }] })
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await touch.waitForFunction(before => window.scrollY !== before, scrollBefore)
    assert(touch.url() === before, 'Scrolling a record does not activate row navigation')
    return { sharedSelection: 'passed', filteredSelectAll: 'passed', narrowCommandsAndFocus: 'passed', touchControlsAndScroll: 'passed' }
  } finally {
    if (touchContext) await touchContext.close()
    for (const project of created) {
      const current = await api(`/projects/${project.id}`, undefined, 'get')
      await api(`/projects/${project.id}/actions/delete`, { expected_revision: current.project.revision, expected_title: current.project.title })
    }
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.getByLabel('Search Projects', { exact: true }).fill('')
    await page.getByLabel('Lifecycle', { exact: true }).click()
    await page.getByRole('option', { name: 'All lifecycles', exact: true }).click()
    await refresh()
  }
}
