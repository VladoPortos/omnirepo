/**
 * Real RAW upload smoke: the dropzone must publish the payload, survive a
 * page reload, and serve the exact uploaded bytes through the protocol route.
 */
import { test, expect } from '@playwright/test';
import {
  adminLoginAPI,
  adminLoginUI,
  resetServerState,
  ADMIN_LOGIN,
  ADMIN_PASSWORD,
} from './helpers/auth';

test.describe('Upload page', () => {
  test.beforeEach(async ({ request }) => {
    await adminLoginAPI(request);
    await resetServerState(request);
    const project = await request.post('/api/v1/projects', {
      data: { name: 'upload-test' },
    });
    expect(project.ok(), await project.text()).toBeTruthy();
    const repo = await request.post('/api/v1/projects/upload-test/repos', {
      data: { name: 'raw-uploads', type: 'raw', auto_scan: false },
    });
    expect(repo.ok(), await repo.text()).toBeTruthy();
  });

  test('upload a file via dropzone', async ({ page, request }) => {
    await adminLoginUI(page);
    await page.goto('/projects/upload-test/raw/raw-uploads');
    const buffer = Buffer.from('test file content for upload e2e');
    const uploaded = page.waitForResponse((response) =>
      response.request().method() === 'PUT' &&
      response.url().endsWith('/test-upload.txt'),
    );
    await page.locator('input[type="file"]').setInputFiles({
      name: 'test-upload.txt',
      mimeType: 'text/plain',
      buffer,
    });
    const response = await uploaded;
    expect(response.ok(), `${response.status()}: ${await response.text()}`).toBeTruthy();

    await page.reload();
    await expect(page.getByRole('row').filter({ hasText: 'test-upload.txt' })).toBeVisible();
    const download = await request.get('/upload-test/raw/raw-uploads/test-upload.txt', {
      headers: {
        Authorization: `Basic ${Buffer.from(`${ADMIN_LOGIN}:${ADMIN_PASSWORD}`).toString('base64')}`,
      },
    });
    expect(download.ok(), await download.text()).toBeTruthy();
    expect(await download.body()).toEqual(buffer);
  });
});
