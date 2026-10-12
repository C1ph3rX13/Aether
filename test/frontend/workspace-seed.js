async page => {
  const api = async (path, data, method = 'post') => {
    const response = await page.request[method](new URL(`/api/v1${path}`, page.url()).href, data ? { data } : undefined)
    if (!response.ok()) throw new Error(`${method} ${path}: HTTP ${response.status()} ${await response.text()}`)
    return response.json()
  }
  for (const entry of await api('/projects', undefined, 'get')) {
    if (entry.project.status === 'active') await api(`/projects/${entry.project.id}/actions/pause`, { expected_revision: entry.project.revision })
  }
  const configuration = await api('/configuration', undefined, 'get')
  const draft = configuration.recommended_draft
  draft.runtime.scheduler_interval_seconds = 1
  draft.runtime.max_project_workers = 1
  draft.container.network_mode = 'host'
  draft.container.image = 'WORKER_IMAGE'
  draft.container.completed_action = 'remove'
  draft.workers = [{ name: 'workspace-bootstrap', agent_type: 'pi', provider_name: 'Controlled Provider', task_types: ['bootstrap', 'reason', 'explore'], max_running: 1, priority: 0, capabilities: [], timeout_overrides: {}, agent_env: { PI_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT/workspace', PI_MODEL: 'fixture', PI_PROVIDER_API: 'openai-completions' } }]
  const saved = await api('/configuration', { expected_draft_version: configuration.draft_version, draft, secrets: { 'workspace-bootstrap': { PI_API_KEY: { value: 'workspace-test-secret' } } } }, 'put')
  const project = await api('/projects', { title: 'Workspace Inspection Acceptance', origin: 'Known Origin evidence', goal: 'Inspect authoritative records and relationships', bootstrap_enabled: true, hints: [{ content: 'Follow the current evidence.' }] })
  await page.evaluate(id => sessionStorage.setItem('workspace-acceptance-id', id), project.project.id)
  return { workspaceSeed: 'passed', projectId: project.project.id, worker: 'local Pi Bootstrap' }
}
