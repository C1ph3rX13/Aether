async page => {
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const expected = [
    ['execution-idle', 'Execution Idle', 'idle', 'Idle'],
    ['execution-reason', 'Execution Reason', 'running', 'Running'],
    ['execution-explore', 'Execution Explore', 'running', 'Running'],
    ['execution-bootstrap', 'Execution Bootstrap', 'running', 'Running'],
    ['execution-retrying', 'Execution Retrying', 'retrying', 'Retrying'],
    ['execution-failed', 'Execution Failed', 'failed_or_interrupted', 'Failed or interrupted'],
    ['execution-exhausted', 'Execution Exhausted', 'retry_exhausted', 'Retry exhausted'],
  ]
  await page.goto(new URL('/projects', page.url()).href)
  await page.getByLabel('Search Projects', { exact: true }).fill('execution-')
  const response = await page.request.get(new URL('/api/v1/projects', page.url()).href)
  assert(response.ok(), 'Real Catalog HTTP read succeeds')
  const catalog = await response.json()
  const table = page.getByRole('table', { name: 'Projects', exact: true })
  for (const [id, title, status, label] of expected) {
    const entry = catalog.find(entry => entry.project.id === id)
    assert(entry && JSON.stringify(entry.execution) === JSON.stringify({ status }), `${id} exposes only the Run-owned status`)
    const row = table.getByRole('row', { name: new RegExp(title) })
    await row.waitFor()
    const execution = row.getByRole('cell').nth(3)
    assert(await execution.innerText() === label, `${id} shows only ${label}`)
    assert(await execution.locator('[data-slot="badge"]').count() === 1, `${id} has exactly one Badge`)
  }
  const idle = catalog.find(entry => entry.project.id === 'execution-idle')
  assert(idle.blackboard.intents.open === 1, 'Idle also covers work waiting for admission')
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 720 }, { width: 320, height: 568 }]) {
    await page.setViewportSize(viewport)
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Execution Catalog fits ${viewport.width}px`)
    for (const [, title, , label] of expected) {
      const cell = table.getByRole('row', { name: new RegExp(title) }).getByRole('cell').nth(3)
      const badge = cell.getByText(label, { exact: true })
      await badge.scrollIntoViewIfNeeded()
      assert(await badge.isVisible(), `${label} remains visible at ${viewport.width}px`)
      assert(await cell.evaluate(element => element.scrollWidth <= element.clientWidth), `${label} fits its cell`)
      const badgeBounds = await badge.boundingBox()
      const scrollBounds = await table.locator('..').boundingBox()
      assert(badgeBounds && scrollBounds && badgeBounds.x >= scrollBounds.x && badgeBounds.x + badgeBounds.width <= scrollBounds.x + scrollBounds.width, `${label} is reachable inside the official table scroll area`)
    }
    await page.evaluate(() => window.scrollTo(0, 0))
    await page.screenshot({ path: `dist/frontend-catalog/execution-${viewport.width}.png`, fullPage: true })
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  return { executionStatuses: 'passed', oneBadge: 'passed', lifecycleIndependence: 'passed', responsiveExecution: 'passed' }
}
