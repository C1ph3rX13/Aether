async page => {
  const closeInspection = async () => {
    const panel = page.getByRole('dialog', { name: 'Project inspection', exact: true })
    if (await panel.isVisible()) { await page.keyboard.press('Escape'); await panel.waitFor({ state: 'hidden' }) }
  }
  const openInspection = async () => {
    if (!await page.getByRole('dialog', { name: 'Project inspection', exact: true }).isVisible())
      await page.getByRole('button', { name: 'Toggle right inspection panel', exact: true }).click()
  }
  const projectAction = async name => {
    await closeInspection()
    if (['Pause', 'Resume', 'Reopen', 'Snapshot'].includes(name)) {
      await page.getByRole('button', { name, exact: true }).click()
    } else {
      await page.getByRole('button', { name: 'Project actions', exact: true }).click()
      await page.getByRole('menuitem', { name: name === 'Delete' ? 'Delete Project' : name, exact: true }).click()
    }
  }
  const origin = new URL(page.url()).origin
  const { id } = await page.evaluate(() => JSON.parse(sessionStorage.getItem('workspace-acceptance')))
  await projectAction('Refresh Project')
  await page.getByText('Project update unavailable', { exact: true }).waitFor({ state: 'hidden' })
  await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  await page.goto(`${origin}/projects/__placeholder__`)
  await page.getByRole('heading', { name: 'Invalid Project', exact: true }).waitFor()
  await page.getByRole('link', { name: 'Return to Projects', exact: true }).click()
  await page.getByRole('table', { name: 'Projects', exact: true }).waitFor()
  await page.goto(`${origin}/projects/proj_missing`)
  await page.getByRole('heading', { name: 'Project Not Found', exact: true }).waitFor()
  const readURL = `${origin}/api/v1/projects/${id}`
  await page.route(readURL, route => route.abort())
  await page.goto(`${origin}/projects/${id}`)
  await page.getByRole('heading', { name: 'Project unavailable', exact: true }).waitFor()
  await page.unroute(readURL)
  await page.getByRole('button', { name: 'Retry Project', exact: true }).click()
  await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  const detail = await (await page.request.get(`${origin}/api/v1/projects/${id}`)).json()
  const response = await page.request.post(`${origin}/api/v1/projects/${id}/actions/delete`, { data: { expected_revision: detail.project.revision, expected_title: detail.project.title } })
  if (!response.ok()) throw new Error(`Isolated Project deletion failed: ${await response.text()}`)
  await page.waitForURL(`${origin}/projects`)
  await page.goto(`${origin}/projects/${id}`)
  await page.getByRole('heading', { name: 'Project Not Found', exact: true }).waitFor()
  return { realWorkspaceRecovery: 'passed', malformedRoute: 'passed', notFoundAndDeletion: 'passed' }
}
