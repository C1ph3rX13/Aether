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
  await projectAction('Refresh Project')
  await page.getByText('Project update unavailable', { exact: true }).waitFor()
  if (!await page.getByRole('application', { name: 'Blackboard graph', exact: true }).isVisible()) throw new Error('Last valid Blackboard remains readable during outage')
  if (await page.getByLabel('Data Freshness: Live', { exact: true }).count()) throw new Error('Failed reads cannot claim Live')
  return { realWorkspaceOutage: 'passed', retainedBlackboard: true }
}
