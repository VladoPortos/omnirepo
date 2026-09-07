import { expect, test } from '@playwright/test';
import { adminLoginAPI, adminLoginUI, resetServerState, ADMIN_LOGIN, ADMIN_PASSWORD } from './helpers/auth';

test.describe('complete repository and project lists', () => {
  test.describe.configure({ timeout: 120_000 });
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
  });

  test('projects beyond page one appear in the list and search selector', async ({ request, page }) => {
    const names = Array.from({ length: 51 }, (_, n) => `page-project-${String(n).padStart(3, '0')}`);
    for (const name of names) {
      const response = await request.post('/api/v1/projects', { data: { name } });
      expect(response.ok(), await response.text()).toBeTruthy();
    }
    const firstResponse = await request.get('/api/v1/projects');
    expect(firstResponse.ok()).toBeTruthy();
    const firstPage = await firstResponse.json() as { items: { name: string }[]; next_cursor: string | null };
    expect(firstPage.next_cursor).toBeTruthy();
    const firstNames = new Set(firstPage.items.map((item) => item.name));
    const beyondFirst = names.find((name) => !firstNames.has(name));
    expect(beyondFirst).toBeTruthy();

    await adminLoginUI(page);
    await page.goto('/projects');
    await expect(page.getByRole('row').filter({ hasText: beyondFirst! })).toBeVisible();
    await page.goto('/search');
    await page.getByRole('combobox').click();
    await expect(page.getByRole('option', { name: beyondFirst!, exact: true })).toBeVisible();
  });

  test('RAW directory browsing includes files beyond the first content page', async ({ request, page }) => {
    const project = 'page-raw-project';
    const repo = 'files';
    const createdProject = await request.post('/api/v1/projects', { data: { name: project } });
    expect(createdProject.ok(), await createdProject.text()).toBeTruthy();
    const createdRepo = await request.post(`/api/v1/projects/${project}/repos`, { data: { name: repo, type: 'raw' } });
    expect(createdRepo.ok(), await createdRepo.text()).toBeTruthy();
    const paths = Array.from({ length: 101 }, (_, n) => `archive/file-${String(n).padStart(3, '0')}.bin`);
    const authorization = `Basic ${Buffer.from(`${ADMIN_LOGIN}:${ADMIN_PASSWORD}`).toString('base64')}`;
    for (const path of paths) {
      const response = await request.put(`/${project}/raw/${repo}/${path}`, {
        data: Buffer.from(path), headers: { Authorization: authorization, 'Content-Type': 'application/octet-stream' },
      });
      expect(response.ok(), await response.text()).toBeTruthy();
    }
    const firstResponse = await request.get(`/api/v1/projects/${project}/repos/raw/${repo}/content`);
    expect(firstResponse.ok()).toBeTruthy();
    const firstPage = await firstResponse.json() as { items: { name: string }[]; next_offset?: number };
    expect(firstPage.next_offset).toBeGreaterThan(0);
    const firstNames = new Set(firstPage.items.map((item) => item.name));
    const beyondFirst = paths.find((path) => !firstNames.has(path));
    expect(beyondFirst).toBeTruthy();

    await adminLoginUI(page);
    await page.goto(`/projects/${project}/raw/${repo}`);
    await page.getByRole('button', { name: 'archive', exact: true }).click();
    await expect(page.getByRole('row').filter({ hasText: beyondFirst!.split('/')[1] })).toBeVisible();
  });
});
