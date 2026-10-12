async page => {
  const selectOption = async (trigger, label) => {
    await trigger.click()
    await page.getByRole('option', { name: label, exact: true }).click()
  }
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const api = async (path, data, method = 'post') => {
    const response = await page.request[method](new URL(`/api/v1${path}`, page.url()).href, data ? { data } : undefined)
    assert(response.ok(), `${method} ${path}: HTTP ${response.status()} ${await response.text()}`)
    return response.json()
  }
  const refresh = async () => {
    const response = page.waitForResponse(response => response.url().endsWith('/api/v1/projects') && response.ok())
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await response
    await page.getByRole('button', { name: 'Refresh', exact: true }).waitFor()
  }
  const firstRow = () => page.getByRole('table', { name: 'Projects', exact: true }).getByRole('row').nth(1)
  await page.getByText('No Projects yet', { exact: true }).waitFor()
  assert(await page.getByRole('navigation', { name: 'Operations navigation', exact: true }).isVisible(), 'Operations navigation has a named landmark')
  assert(await page.getByLabel('Total Projects: 0', { exact: true }).isVisible(), 'Empty Catalog total')

  const current = await api('/configuration', undefined, 'get')
  const draft = current.recommended_draft
  draft.workers = [{
    name: 'catalog-worker', agent_type: 'codex', provider_name: 'Controlled Provider',
    task_types: ['bootstrap', 'reason', 'explore'], max_running: 1, priority: 0,
    capabilities: [], timeout_overrides: {},
    agent_env: { CODEX_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT', CODEX_MODEL: 'test-model' },
  }]
  const saved = await api('/configuration', {
    expected_draft_version: current.draft_version, draft,
    secrets: { 'catalog-worker': { OPENAI_API_KEY: { value: 'catalog-test-secret' } } },
  }, 'put')
  const projects = []
  for (let index = 0; index < 16; index++) {
    const hintCount = index === 0 ? 2 : index === 1 ? 10 : 0
    const detail = await api('/projects', {
      title: `Catalog Project ${String(index + 1).padStart(2, '0')}`,
      origin: 'Known acceptance evidence', goal: 'Verify Catalog behavior', bootstrap_enabled: false,
      hints: Array.from({ length: hintCount }, (_, number) => ({ content: `Hint ${number + 1}` })),
    })
    if (index % 3 === 1) await api(`/projects/${detail.project.id}/actions/pause`, { expected_revision: detail.project.revision })
    if (index % 3 === 2) await api(`/projects/${detail.project.id}/actions/complete`, {
      expected_revision: detail.project.revision, from: ['origin'], description: 'Acceptance completion evidence',
    })
    projects.push(detail.project)
    // The public Project timestamp has second precision; keep the oldest entry distinct.
    if (index === 0) await new Promise(resolve => setTimeout(resolve, 1100))
  }
  await refresh()
  await page.getByText('16 of 16 Projects', { exact: true }).waitFor()
  assert(!(await firstRow().innerText()).includes(projects[15].id), 'Catalog displays names without internal IDs')
  const createdText = await firstRow().locator('time').innerText()
  assert(/[A-Za-z]/.test(createdText) && !/[\u3400-\u9fff]/.test(createdText), 'Created stays English in a Chinese browser locale')
  for (const [label, count] of [['Total Projects', 16], ['Active', 6], ['Paused', 5], ['Completed', 5]]) {
    assert(await page.getByLabel(`${label}: ${count}`, { exact: true }).isVisible(), `Complete Catalog ${label}`)
  }
  await page.getByText('Project creation ready', { exact: true }).waitFor()
  await page.getByText('New Projects will wait until execution returns.', { exact: true }).waitFor()
  assert(!await page.getByText('catalog-operator', { exact: true }).isVisible(), 'Shell does not expose backend identity')
  assert(await firstRow().getByLabel('Facts: 2 active of 2 total').isVisible(), 'Real Blackboard current/total counts')
  const executionCell = firstRow().getByRole('cell').nth(3)
  assert(await executionCell.innerText() === 'Idle', 'Run-owned idle does not infer pending work')
  assert(await executionCell.locator('[data-slot="badge"]').count() === 1, 'One Execution Badge')

  assert(await page.getByRole('columnheader', { name: /Actions/ }).isVisible(), 'C Catalog retains centered action column')
  await page.getByRole('button', { name: 'Sort by Created', exact: true }).click()
  assert(Date.parse(await firstRow().locator('time').getAttribute('datetime')) === Math.max(...projects.map(project => Date.parse(project.created_at))), 'Newest creation first')
  await page.getByRole('button', { name: 'Sort by Created', exact: true }).click()
  assert((await firstRow().innerText()).includes('Catalog Project 01'), 'Oldest creation first')
  await page.getByLabel('Search Projects', { exact: true }).fill(projects[0].id)
  await page.getByText('1 of 16 Projects', { exact: true }).waitFor()
  assert((await firstRow().innerText()).includes('Catalog Project 01'), 'Search stable Project ID')
  await page.getByLabel('Search Projects', { exact: true }).fill('Catalog Project')
  await selectOption(page.getByLabel('Lifecycle', { exact: true }), 'Paused')
  await page.getByText('5 of 16 Projects', { exact: true }).waitFor()
  assert(await page.getByLabel('Total Projects: 16', { exact: true }).isVisible(), 'Stats unchanged by local filtering')
  await refresh()
  assert(await page.getByLabel('Search Projects', { exact: true }).inputValue() === 'Catalog Project', 'Refresh preserves search')
  assert(await page.getByLabel('Lifecycle', { exact: true }).textContent() === 'Paused', 'Refresh preserves lifecycle filter')
  await page.getByLabel('Search Projects', { exact: true }).fill('missing-project')
  await page.getByText('No matching Projects', { exact: true }).waitFor()
  assert(!await page.getByText('No Projects yet', { exact: true }).isVisible(), 'No match is distinct from empty')
  await page.getByRole('button', { name: 'Clear filters', exact: true }).click()
  await page.getByRole('button', { name: 'Go to last page', exact: true }).click()
  await page.getByText('Page 2 of 2', { exact: true }).waitFor()
  await refresh()
  assert(await page.getByText('Page 2 of 2', { exact: true }).isVisible(), 'Normal refresh preserves page')
  assert(await page.getByRole('columnheader', { name: /Created/ }).getAttribute('aria-sort') === 'ascending', 'Refresh preserves date sorting')

  const catalog = await api('/projects', undefined, 'get')
  for (const entry of catalog.slice(9)) await api(`/projects/${entry.project.id}/actions/delete`, {
    expected_title: entry.project.title, expected_revision: entry.project.revision,
  })
  await refresh()
  await page.getByText('Page 1 of 1', { exact: true }).waitFor()
  await page.getByText('9 of 9 Projects', { exact: true }).waitFor()
  assert(await page.getByRole('table', { name: 'Projects', exact: true }).getByRole('row').count() === 10, 'Invalid page recovered after real deletion')

  await page.setViewportSize({ width: 1024, height: 720 })
  assert(await page.getByRole('table', { name: 'Projects', exact: true }).isVisible(), 'Minimum desktop Catalog visible')
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'No page overflow at 1024 × 720')
  assert(await page.getByRole('table', { name: 'Projects', exact: true }).evaluate(table => [...table.querySelectorAll('th, td')].every(cell => cell.scrollWidth <= cell.clientWidth)), 'Lifecycle badges and controls fit their columns at minimum desktop size')
  await page.getByLabel('Search Projects', { exact: true }).focus()
  await page.keyboard.press('Tab')
  assert(await page.getByLabel('Lifecycle', { exact: true }).evaluate(element => element === document.activeElement), 'Keyboard navigation reaches lifecycle filter')
  assert(await page.getByLabel('Lifecycle', { exact: true }).evaluate(element => getComputedStyle(element).boxShadow !== 'none'), 'Keyboard focus visible')
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'No page overflow with 200% text at 1024 × 720')
  assert(await page.getByRole('main').evaluate(element => element.scrollWidth <= element.clientWidth), 'Catalog controls wrap within the main content with 200% text')
  const statistics = page.getByRole('group', { name: 'Project lifecycle statistics', exact: true })
  for (const label of ['Total Projects', 'Active', 'Paused', 'Completed']) {
    const card = statistics.getByLabel(new RegExp(`^${label}:`))
    const cardBounds = await card.boundingBox()
    const labelBounds = await card.getByText(label, { exact: true }).boundingBox()
    assert(cardBounds && labelBounds && labelBounds.x >= cardBounds.x && labelBounds.x + labelBounds.width <= cardBounds.x + cardBounds.width, `${label} remains readable with 200% text`)
    assert(await card.getByText(label, { exact: true }).evaluate(element => element.scrollWidth <= element.clientWidth), `${label} is not clipped with 200% text`)
  }
  await page.screenshot({ path: 'dist/frontend-catalog/catalog-text-200-1024.png', fullPage: true })
  await page.evaluate(() => { document.documentElement.style.removeProperty('font-size') })
  for (const viewport of [{ width: 320, height: 568 }, { width: 390, height: 844 }, { width: 768, height: 1024 }, { width: 844, height: 390 }, { width: 1023, height: 720 }, { width: 1024, height: 719 }]) {
    await page.setViewportSize(viewport)
    await page.getByRole('table', { name: 'Projects', exact: true }).waitFor()
    const layout = await firstRow().evaluate(row => ({
      width: row.closest('section').clientWidth,
      breakpoint: 40 * parseFloat(getComputedStyle(document.documentElement).fontSize),
      display: getComputedStyle(row).display,
    }))
    assert(layout.display === (layout.width < layout.breakpoint ? 'grid' : 'table-row'), 'Catalog layout follows available container width')
    if (layout.width < layout.breakpoint) {
      assert(await firstRow().locator('td').count() === 8, 'Reflow retains one set of record cells and controls')
      for (const label of ['Lifecycle', 'Execution', 'Facts', 'Intents', 'Created', 'Actions']) {
        assert(await firstRow().getByText(label, { exact: true }).isVisible(), `${label} labels the narrow record`)
      }
      assert(await firstRow().evaluate(row => [...row.querySelectorAll('td')].every(cell => cell.scrollWidth <= cell.clientWidth)), 'Narrow records need no horizontal scrolling')
      for (const label of ['Project', 'Facts', 'Intents', 'Created']) {
        assert(await page.getByRole('button', { name: `Sort by ${label}`, exact: true }).isVisible(), `${label} sorting remains available`)
      }
      assert(await page.getByRole('checkbox', { name: 'Select all visible Projects', exact: true }).isVisible(), 'Filtered select-all remains available')
    }
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Catalog fits ${viewport.width} × ${viewport.height}`)
    assert(await page.getByLabel('Rows per page', { exact: true }).evaluate(element => element.clientWidth - parseFloat(getComputedStyle(element).paddingLeft) - parseFloat(getComputedStyle(element).paddingRight) > 12), 'Page size remains readable')
  }
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: 'Toggle Sidebar', exact: true }).click()
  const drawer = page.getByRole('dialog', { name: 'Operations navigation', exact: true })
  await drawer.waitFor()
  assert(await drawer.getByRole('navigation', { name: 'Operations navigation', exact: true }).isVisible(), 'Phone navigation retains its named landmark')
  await drawer.getByRole('link', { name: 'Projects', exact: true }).click()
  await drawer.waitFor({ state: 'hidden' })
  assert(await page.getByRole('table', { name: 'Projects', exact: true }).isVisible(), 'Selecting a route closes the drawer')
  await page.screenshot({ path: 'dist/frontend-catalog/catalog-390.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.getByRole('table', { name: 'Projects', exact: true }).waitFor()
  return { catalogRead: 'passed', actualProjects: 16, afterDeletion: 9, responsiveViewports: 'passed', textResize: 'passed', navigationLandmark: 'passed' }
}
