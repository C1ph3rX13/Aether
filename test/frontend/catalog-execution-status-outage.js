async page => {
  const table = page.getByRole('table', { name: 'Projects', exact: true })
  const cells = table.getByRole('row').locator('td:nth-child(4)')
  const before = await cells.allTextContents()
  if (before.length !== 7) throw new Error('All seeded execution rows must be loaded before outage')
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await page.getByText('Catalog update unavailable', { exact: true }).waitFor()
  if (JSON.stringify(await cells.allTextContents()) !== JSON.stringify(before)) throw new Error('Failed refresh must retain every last valid execution status')
  if (await cells.locator('[data-slot="badge"]').count() !== 7) throw new Error('Failed refresh must retain one Badge per execution cell')
  return { retainedExecutionStatuses: 'passed' }
}
