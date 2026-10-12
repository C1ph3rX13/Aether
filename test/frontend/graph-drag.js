async page => {
  const assert = (ok, message) => { if (!ok) throw new Error(message) }
  const root = new URL(page.url()).origin
  const path = new URL(page.url()).pathname
  const project = path.split('/').pop()
  const response = await page.request.get(`${root}/api/v1/projects/${project}`)
  assert(response.ok(), 'Project is available')
  const detail = await response.json()
  const edgeCount = detail.intents.reduce((n, intent) => n + intent.from.length + (intent.to ? 1 : 0), 0) + detail.facts.filter(fact => fact.supersedes).length
  assert(edgeCount > 0, 'Drag regression requires a connected Project')
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  const origin = page.locator('.react-flow__node[data-id="fact:origin"]')
  const position = () => origin.evaluate(node => node.style.transform)
  const shapes = () => page.locator('.react-flow__edge-path').evaluateAll(edges => edges.map(edge => edge.getAttribute('d')))
  const captures = []
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto(root + path)
    await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
    await page.waitForFunction(count => document.querySelectorAll('.react-flow__edge-path').length === count, edgeCount)
    const graph = page.getByRole('application', { name: 'Blackboard graph', exact: true })
    const canvas = await page.getByTestId('project-canvas').boundingBox()
    for (const direction of ['Left right', 'Top down']) {
      await page.getByRole('radio', { name: direction, exact: true }).click()
      await page.waitForTimeout(100)
      const arranged = await position()
      await page.getByRole('button', { name: 'Actual size', exact: true }).click()
      await origin.scrollIntoViewIfNeeded()
      const before = await position()
      const paths = await shapes()
      const header = await origin.locator('[data-slot="card-header"]').boundingBox()
      assert(header, 'Card header is visible')
      await page.mouse.move(header.x + 40, header.y + header.height / 2)
      await page.mouse.down()
      for (let step = 1; step <= 12; step++) {
        await page.mouse.move(header.x + 40 + step * 3, header.y + header.height / 2 + step * 2)
        assert(await graph.locator('.react-flow__edge-path').count() === edgeCount, 'Connections remain visible throughout drag')
      }
      await page.mouse.up()
      await page.waitForTimeout(80)
      const moved = await position()
      assert(moved !== before, 'Card retains its moved coordinates after release')
      assert(JSON.stringify(await shapes()) !== JSON.stringify(paths), 'Connections follow the moved card')
      assert(!await page.getByRole('dialog', { name: 'Project inspection', exact: true }).isVisible(), 'Dragging does not open inspection')
      await origin.getByRole('button', { name: 'Open Fact origin', exact: true }).click()
      await page.getByRole('dialog', { name: 'Project inspection', exact: true }).waitFor()
      await page.keyboard.press('Escape')
      assert(await position() === moved, 'Inspecting a card preserves its manual position')
      assert(await origin.evaluate(node => node.offsetWidth === 240 && node.offsetHeight === 160), 'Card dimensions remain unchanged')
      await page.getByRole('button', { name: 'Re-layout graph', exact: true }).click()
      await page.waitForFunction(({ arranged }) => document.querySelector('.react-flow__node[data-id="fact:origin"]').style.transform === arranged, { arranged })
      assert(await graph.locator('.react-flow__edge-path').count() === edgeCount, 'Re-layout retains all connections')
      captures.push({ width, direction, before, moved, restored: await position(), edges: edgeCount })
    }
    await page.getByTestId('project-canvas').scrollIntoViewIfNeeded()
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'No page overflow')
    const after = await page.getByTestId('project-canvas').boundingBox()
    assert(after.width === canvas.width && after.height === canvas.height, 'Dragging preserves canvas size')
    await page.screenshot({ path: `.impeccable/review/graph-drag/after-${width}.png`, fullPage: true, animations: 'disabled' })
  }
  assert(errors.length === 0, 'No application errors')
  return { project, captures, errors, businessWrites: false }
}
