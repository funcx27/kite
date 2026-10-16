import {
  expect,
  test,
  type APIRequestContext,
  type APIResponse,
} from "@playwright/test";

import { authFile } from "../../env";

test.use({ storageState: authFile });

// Platform module API contracts, asserted at the request level (no UI):
// applications, workflow, buildfleet and kcross. Request shapes follow
// modules/application/internal/api, modules/workflow/internal/api,
// pkg/buildfleetadapter and pkg/kcross. Every write operates on fixtures this
// spec created (unique e2e-<ts> prefix) and tears them down again; the only
// writes without fixtures are validation-failure paths proven non-mutating in
// the handlers (400 is written before any persistence).

interface ClusterView {
  name: string;
  error?: string;
}

interface AppResponse {
  id: number;
  name: string;
  namespace: string;
  description: string;
  configs: Array<{
    id: number;
    cluster: string;
    namespace: string;
    statusSummary: string;
  }>;
  deploy?: { success: boolean };
}

interface WorkflowResponse {
  id: number;
  name: string;
  description: string;
  currentRev: number;
  spec?: unknown;
  params?: unknown;
  triggers?: unknown;
  createdAt: string;
  updatedAt: string;
}

async function expectJSON<T>(
  response: APIResponse,
  status: number,
): Promise<T> {
  const text = await response.text();
  expect(response.status(), text).toBe(status);
  return JSON.parse(text) as T;
}

async function expectErrorBody(response: APIResponse, status: number) {
  const body = await expectJSON<{ error: unknown }>(response, status);
  expect(body.error, "error body carries a message").toBeTruthy();
  return body;
}

// Minimal renderable AppSpec (render/spec.go Validate: deployment, image,
// cpu+memory limits mandatory).
function appSpec() {
  return {
    workload: {
      type: "deployment",
      image: "registry.k8s.io/pause:3.10.1",
      replicas: 1,
      limits: { cpu: "100m", memory: "64Mi" },
    },
  };
}

// Clusters that can actually take a publish: the overview probe fails for
// registered-but-unreachable clusters, which a bare GET /clusters cannot
// distinguish. Cluster names are always resolved dynamically.
async function deployableClusters(request: APIRequestContext) {
  const clusters = await expectJSON<ClusterView[]>(
    await request.get("/api/v1/clusters"),
    200,
  );
  const usable: string[] = [];
  for (const name of clusters.map((cluster) => cluster.name).sort()) {
    const overview = await request
      .get(`/api/v1/_clusters/${encodeURIComponent(name)}/overview`)
      .catch(() => undefined);
    if (overview?.ok()) usable.push(name);
  }
  return usable;
}

test.describe("platform module API contracts", () => {
  // ── applications ──
  // The list row's configs[].cluster/statusSummary fields only exist on apps
  // with at least one cluster config, so this test publishes its own app and
  // asserts the shapes against it; the 404 shape and the quota upsert's
  // succeeded/failed structure ride the same fixtures.
  test("applications: list shape, single 404 and space quota upsert", async ({
    request,
  }) => {
    const suffix = Date.now().toString(36);
    const space = `e2e-${suffix}-api`;
    const app = `e2e-${suffix}-app`;
    const bogusCluster = `e2e-${suffix}-bogus`;

    const cluster = (await deployableClusters(request))[0];
    test.skip(
      !cluster,
      "applications list shape needs one deployable cluster to publish a config",
    );

    // Quota upsert: one real + one unknown cluster in the same request —
    // per-cluster failures ride the 200 body, not the status code
    // (spaces_api.go UpsertSpaceQuotas).
    const upsert = await expectJSON<{
      succeeded: string[];
      failed: Array<{ cluster: string; error: string }>;
    }>(
      await request.put(`/api/v1/applications/spaces/${space}/quotas`, {
        data: {
          quotas: [
            { cluster, cpu: "1", memory: "1Gi" },
            { cluster: bogusCluster, cpu: "1", memory: "1Gi" },
          ],
        },
      }),
      200,
    );
    expect(upsert.succeeded).toEqual([cluster]);
    expect(upsert.failed).toHaveLength(1);
    expect(upsert.failed[0].cluster).toBe(bogusCluster);
    expect(typeof upsert.failed[0].error).toBe("string");
    expect(upsert.failed[0].error.length).toBeGreaterThan(0);

    // Body validation failures are 400 before any cluster is touched.
    await expectErrorBody(
      await request.put(`/api/v1/applications/spaces/${space}/quotas`, {
        data: { quotas: [] },
      }),
      400,
    );
    await expectErrorBody(
      await request.put(`/api/v1/applications/spaces/${space}/quotas`, {
        data: {
          quotas: [{ cluster, cpu: "not-a-quantity", memory: "1Gi" }],
        },
      }),
      400,
    );

    // Create with a target cluster first-publishes the draft (deploy outcome
    // rides the response's deploy field).
    const created = await expectJSON<AppResponse>(
      await request.post("/api/v1/applications", {
        data: {
          name: app,
          namespace: space,
          description: `platform api shape fixture ${suffix}`,
          spec: appSpec(),
          cluster,
        },
      }),
      201,
    );
    expect(created.deploy?.success, "first publish on create").toBe(true);

    try {
      // GET list: envelope plus the config summary fields of our own row.
      const list = await expectJSON<{
        applications: AppResponse[];
        total: number;
      }>(await request.get("/api/v1/applications"), 200);
      expect(Array.isArray(list.applications)).toBe(true);
      expect(list.total).toBe(list.applications.length);
      const mine = list.applications.find(
        (entry) => entry.namespace === space && entry.name === app,
      );
      expect(mine, "created app is listed").toBeTruthy();
      expect(mine!.configs).toHaveLength(1);
      expect(mine!.configs[0].cluster).toBe(cluster);
      expect(mine!.configs[0].namespace).toBe(space);
      expect(typeof mine!.configs[0].statusSummary).toBe("string");
      expect(typeof mine!.configs[0].id).toBe("number");

      // GET single: the live app resolves, a missing one answers the
      // module's 404 error envelope (apps_api.go findAppOr404).
      const single = await expectJSON<AppResponse>(
        await request.get(
          `/api/v1/applications/${encodeURIComponent(space)}/${encodeURIComponent(app)}`,
        ),
        200,
      );
      expect(single.id).toBe(mine!.id);
      const missing = await expectErrorBody(
        await request.get(
          `/api/v1/applications/${encodeURIComponent(space)}/e2e-${suffix}-missing`,
        ),
        404,
      );
      expect(typeof missing.error).toBe("string");

      // The quota view reflects the upserted hard values as raw quantity
      // strings (spaces_api.go GetSpaceQuotas); the unknown cluster has no
      // space namespace and is skipped.
      const quotas = await expectJSON<{
        quotas: Array<{ cluster: string; cpu: string; memory: string }>;
      }>(await request.get(`/api/v1/applications/spaces/${space}/quotas`), 200);
      const row = quotas.quotas.find((entry) => entry.cluster === cluster);
      expect(row, "quota row for the seeded cluster").toBeTruthy();
      expect(row!.cpu).toBe("1");
      expect(row!.memory).toBe("1Gi");
    } finally {
      await cleanupApp(request, space, app, cluster);
      await dissociateSpace(request, space, [cluster]);
    }
  });

  // ── workflow ──
  // Definition list shape plus the full CRUD round trip of a minimal
  // definition (an empty draft or a single-node DAG validates, spec.go
  // Validate) and the webhook token get/reset rotation.
  test("workflow: list shape, definition round trip and webhook token rotation", async ({
    request,
  }) => {
    const suffix = Date.now().toString(36);
    const name = `e2e-${suffix}-wf`;
    const specV1 = {
      nodes: [
        {
          id: "notify",
          type: "notify",
          name: "done",
          props: { url: "https://e2e.invalid/hook" },
        },
      ],
      edges: [],
    };
    const specV2 = {
      nodes: [
        {
          id: "n1",
          type: "notify",
          name: "first",
          props: { url: "https://e2e.invalid/1" },
        },
        {
          id: "n2",
          type: "notify",
          name: "second",
          props: { url: "https://e2e.invalid/2" },
        },
      ],
      edges: [{ from: "n1", to: "n2" }],
    };
    let workflowId: number | undefined;

    try {
      const list = await expectJSON<{
        workflows: WorkflowResponse[];
        total: number;
      }>(await request.get("/api/v1/workflows"), 200);
      expect(Array.isArray(list.workflows)).toBe(true);
      expect(list.total).toBe(list.workflows.length);

      const created = await expectJSON<WorkflowResponse>(
        await request.post("/api/v1/workflows", {
          data: {
            name,
            description: `platform api fixture ${suffix}`,
            spec: specV1,
          },
        }),
        201,
      );
      workflowId = created.id;
      expect(created.id).toBeGreaterThan(0);
      expect(created.name).toBe(name);
      expect(created.currentRev).toBe(1);
      expect(created.spec).toEqual(specV1);

      const fetched = await expectJSON<WorkflowResponse>(
        await request.get(`/api/v1/workflows/${created.id}`),
        200,
      );
      expect(fetched.name).toBe(name);
      expect(fetched.currentRev).toBe(1);
      expect(fetched.spec).toEqual(specV1);

      const listAfter = await expectJSON<{
        workflows: Array<{ id: number }>;
        total: number;
      }>(await request.get("/api/v1/workflows"), 200);
      expect(
        listAfter.workflows.some((entry) => entry.id === created.id),
        "created definition is listed",
      ).toBe(true);

      // Update under the optimistic lock: the revision chain advances and a
      // stale expectedRev is refused with 409 (workflows_api.go).
      const updated = await expectJSON<WorkflowResponse>(
        await request.put(`/api/v1/workflows/${created.id}`, {
          data: {
            description: `updated ${suffix}`,
            spec: specV2,
            expectedRev: 1,
          },
        }),
        200,
      );
      expect(updated.currentRev).toBe(2);
      expect(updated.description).toBe(`updated ${suffix}`);
      expect(updated.spec).toEqual(specV2);
      await expectErrorBody(
        await request.put(`/api/v1/workflows/${created.id}`, {
          data: { expectedRev: 1 },
        }),
        409,
      );

      // Webhook token: 128-bit hex at creation, rotation issues a new one and
      // the rotated value is what a subsequent read returns.
      const tokenPattern = /^[0-9a-f]{32}$/;
      const before = await expectJSON<{ token: string }>(
        await request.get(`/api/v1/workflows/${created.id}/webhook-token`),
        200,
      );
      expect(before.token).toMatch(tokenPattern);
      const reset = await expectJSON<{ token: string }>(
        await request.post(
          `/api/v1/workflows/${created.id}/webhook-token/reset`,
        ),
        200,
      );
      expect(reset.token).toMatch(tokenPattern);
      expect(reset.token).not.toBe(before.token);
      const after = await expectJSON<{ token: string }>(
        await request.get(`/api/v1/workflows/${created.id}/webhook-token`),
        200,
      );
      expect(after.token).toBe(reset.token);

      const deleted = await expectJSON<{ message: string }>(
        await request.delete(`/api/v1/workflows/${created.id}`),
        200,
      );
      expect(typeof deleted.message).toBe("string");
      workflowId = undefined;
      await expectErrorBody(
        await request.get(`/api/v1/workflows/${created.id}`),
        404,
      );
    } finally {
      if (workflowId !== undefined) {
        await request
          .delete(`/api/v1/workflows/${workflowId}`)
          .catch(() => undefined);
      }
    }
  });

  // ── buildfleet ──
  // GET surfaces only: the pool status, the cache stat (200 with usage or
  // 501 when no provider enables a cache registry — adapter handleCacheStat)
  // and the build history, which the module persists as audit rows with
  // resourceType build (adapter.go recordHistory). The write path is covered
  // only through its parameter-validation failure: an empty build request is
  // rejected with 400 before any build is placed (toFleetRequest).
  test("buildfleet: status, cache stat and build history shapes", async ({
    request,
  }) => {
    const status = await expectJSON<{
      runtimes: Array<{ id: string; state: string; provider: string }>;
      tasks: Array<{ runtimeId: string }>;
    }>(await request.get("/api/v1/buildfleet/status"), 200);
    expect(Array.isArray(status.runtimes)).toBe(true);
    expect(Array.isArray(status.tasks)).toBe(true);
    for (const runtime of status.runtimes) {
      expect(typeof runtime.id).toBe("string");
      expect(typeof runtime.state).toBe("string");
      expect(typeof runtime.provider).toBe("string");
    }

    const cacheStat = await request.get("/api/v1/buildfleet/cachestat");
    const cacheText = await cacheStat.text();
    const cacheBody = JSON.parse(cacheText) as {
      totalBytes?: number;
      projects?: unknown[];
      error?: unknown;
    };
    if (cacheStat.status() === 200) {
      expect(typeof cacheBody.totalBytes).toBe("number");
      expect(Array.isArray(cacheBody.projects)).toBe(true);
    } else {
      expect(cacheStat.status(), cacheText).toBe(501);
      expect(typeof cacheBody.error).toBe("string");
    }

    const history = await expectJSON<{
      data: Array<{ resourceType: string; operationSource: string }>;
      total: number;
    }>(
      await request.get(
        "/api/v1/admin/audit-logs?resourceType=build&page=1&size=20",
      ),
      200,
    );
    expect(Array.isArray(history.data)).toBe(true);
    expect(typeof history.total).toBe("number");
    for (const row of history.data) {
      expect(row.resourceType).toBe("build");
      expect(row.operationSource).toBe("buildfleet");
    }

    const rejected = await expectErrorBody(
      await request.post("/api/v1/buildfleet/builds", { data: {} }),
      400,
    );
    expect(String(rejected.error)).toContain("required");
  });

  // ── kcross ──
  // Read-only shapes plus the non-mutating invalid-gateway path: the PUT
  // handler validates every gateway prefix before persisting anything
  // (handler.go HandleUpdateSetting), so the 400 must leave the stored
  // setting untouched.
  test("kcross: setting and peers shapes, invalid gateway PUT refused", async ({
    request,
  }) => {
    const setting = await expectJSON<{
      enabled: boolean;
      gateways: string[] | null;
      adminToken: string;
      adminTokenConfigured: boolean;
    }>(await request.get("/api/v1/admin/kcross/setting"), 200);
    expect(typeof setting.enabled).toBe("boolean");
    expect(typeof setting.adminTokenConfigured).toBe("boolean");
    expect(setting.adminToken).toBe("");
    expect(setting.gateways === null || Array.isArray(setting.gateways)).toBe(
      true,
    );

    // Discovery depends on the integration being configured: 200 with the
    // peer list, or 400 with the error envelope when it is not (client.go
    // Discover). Either way the body shape is asserted, nothing is mutated.
    const discover = await request.get("/api/v1/admin/kcross/discover");
    const discoverText = await discover.text();
    const discoverBody = JSON.parse(discoverText) as {
      peers?: Array<{ id: string; capabilities: unknown[]; bound: boolean }>;
      error?: unknown;
    };
    if (discover.status() === 200) {
      expect(Array.isArray(discoverBody.peers), discoverText).toBe(true);
      for (const peer of discoverBody.peers!) {
        expect(typeof peer.id).toBe("string");
        expect(Array.isArray(peer.capabilities)).toBe(true);
        expect(typeof peer.bound).toBe("boolean");
      }
    } else {
      expect(discover.status(), discoverText).toBe(400);
      expect(typeof discoverBody.error).toBe("string");
    }

    await expectErrorBody(
      await request.put("/api/v1/admin/kcross/setting", {
        data: { gateways: "e2e-not-a-url" },
      }),
      400,
    );
    const afterRejected = await expectJSON<typeof setting>(
      await request.get("/api/v1/admin/kcross/setting"),
      200,
    );
    expect(afterRejected).toEqual(setting);
  });
});

// ── fixture teardown (shapes from modules/application/internal/api) ──

// Offline the config first (the delete gate refuses deployed apps), then
// delete the application row. Best effort so a mid-test failure still
// tears down what exists.
async function cleanupApp(
  request: APIRequestContext,
  space: string,
  app: string,
  cluster: string,
) {
  await request
    .post(
      `/api/v1/applications/${encodeURIComponent(space)}/${encodeURIComponent(app)}/configs/${encodeURIComponent(cluster)}/offline`,
    )
    .catch(() => undefined);
  await request
    .delete(
      `/api/v1/applications/${encodeURIComponent(space)}/${encodeURIComponent(app)}`,
    )
    .catch(() => undefined);
}

// Strip the space marker from the fixture namespace; the namespace itself
// stays, the space just leaves the picker.
async function dissociateSpace(
  request: APIRequestContext,
  space: string,
  clusters: string[],
) {
  await request
    .delete(`/api/v1/applications/spaces/${encodeURIComponent(space)}`, {
      data: { clusters },
    })
    .catch(() => undefined);
}
