import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
	ProjectAgentsSettingsView,
	ProjectGeneralSettingsView,
	ProjectSettingsFormView,
	ProjectSettingsSection,
	ProjectWorkflowSettingsView,
	validateProjectSettings,
} from "@openagents/product-ui";
import { useEffect, useState } from "react";
import { Info, Pencil } from "lucide-react";
import type { components } from "../../api/schema";
import {
	agentModelsQueryKey,
	agentModelsQueryOptions,
	refreshAgentModels,
	revalidateAgentModels,
	type AgentModelCatalog,
} from "../hooks/useAgentModelsQuery";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { ManagerSpawnError, spawnManager } from "../lib/spawn-manager";
import { type ManagerReplacementFailure, useUiStore } from "../stores/ui-store";
import { newestActiveManager } from "../types/workspace";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { buildIntake, deriveRepoPath, deriveRepoHost, IntakeFields, type IntakeForm } from "./IntakeFields";
import { ProductExternalLink } from "./ProductExternalLink";
import { ReviewerSelect, reviewerTrustWarning } from "./ReviewerSelect";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { SettingsRow } from "./settings/SettingsRow";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type Project = components["schemas"]["Project"];
type ProjectConfig = components["schemas"]["ProjectConfig"];
type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

const PERMISSION_MODE_VALUES = ["default", "accept-edits", "auto", "bypass-permissions"] as const;
const DEFAULT_BRANCH_AUTO = "auto";

const projectQueryKey = (id: string) => ["project", id] as const;

type SettingsSaveResult = {
	replacementError: string | null;
	replacementSessionId: string | null;
	replacementFailure: ManagerReplacementFailure | null;
	spawnError: unknown;
};

export type ProjectSettingsSection = "general" | "agents" | "workflow" | "intake";
export type ProjectSettingsSaveState = {
	phase: "idle" | "pending" | "saving" | "saved" | "failed";
	error?: string;
	replacementError?: string;
};

export function ProjectSettingsForm({
	projectId,
	section = "general",
	onSaveState,
}: {
	projectId: string;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const queryClient = useQueryClient();

	const query = useQuery({
		queryKey: projectQueryKey(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (data?.status !== "ok") throw new Error("Project config is unavailable (degraded).");
			return data.project as Project;
		},
	});

	return (
		<>
			{query.isLoading ? (
				<p className="text-sm text-settings-muted">{"Loading project settings…"}</p>
			) : query.isError || !query.data ? (
				<p className="text-sm text-error">
					{query.error instanceof Error ? query.error.message : "Could not load project."}
				</p>
			) : (
				<SettingsBody
					key={projectId}
					project={query.data}
					onSaved={() =>
						queryClient.invalidateQueries({ queryKey: workspaceQueryKey }).catch(() => {
							// Saving succeeds even if the cache refresh fails.
						})
					}
					projectId={projectId}
					section={section}
					onSaveState={onSaveState}
				/>
			)}
		</>
	);
}

function SettingsBody({
	project,
	projectId,
	onSaved,
	section = "general",
	onSaveState,
}: {
	project: Project;
	projectId: string;
	onSaved: () => Promise<void>;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const closeSettings = useUiStore((state) => state.closeSettings);
	const setManagerReplacementError = useUiStore((state) => state.setManagerReplacementError);
	const workspaceQuery = useWorkspaceQuery();
	const config = project.config ?? {};
	const isScratchProject = project.kind === "scratch";
	const workspace = workspaceQuery.data?.find((item) => item.id === projectId);
	const activeManager = newestActiveManager(workspace?.sessions ?? []);
	const intake: TrackerIntakeConfig = config.trackerIntake ?? {};
	const [form, setForm] = useState({
		displayName: project.name,
		defaultBranch: config.defaultBranch ?? DEFAULT_BRANCH_AUTO,
		sessionPrefix: config.sessionPrefix ?? "",
		workerAgent: config.worker?.agent ?? "",
		managerAgent: config.manager?.agent ?? "",
		workerModel: config.worker?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		workerPermissions: config.worker?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		managerModel: config.manager?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		managerPermissions: config.manager?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		workerMode: config.worker?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		managerMode: config.manager?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		reviewerHarness: config.reviewers?.[0]?.harness ?? "",
		reviewerModel: config.reviewers?.[0]?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		reviewerMode: config.reviewers?.[0]?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		reviewerPermissions: config.reviewers?.[0]?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		autoReview: config.autoReview ?? false,
		intakeEnabled: intake.enabled ?? false,
		intakeRepo: intake.repo ?? "",
		intakeAssignee: intake.assignee ?? "",
	});
	const [savedAt, setSavedAt] = useState<number | null>(null);
	const [showSaving, setShowSaving] = useState(false);
	const [replacementError, setReplacementError] = useState<string | null>(null);
	const [validationError, setValidationError] = useState<string | null>(null);
	const initialManagerAgent = config.manager?.agent ?? "";
	const missingRequiredAgent = form.workerAgent === "" || form.managerAgent === "";
	const agentsQuery = useAgentReadinessQuery();
	useEnsureAgentReadiness();
	useEnsureAgentReadiness({
		agentIds: [form.workerAgent, form.managerAgent, form.reviewerHarness],
		enabled: form.workerAgent !== "" || form.managerAgent !== "" || form.reviewerHarness !== "",
	});
	const agentCatalog = agentsQuery.data;

	const intakeForm: IntakeForm = {
		enabled: form.intakeEnabled,
		repo: form.intakeRepo,
		assignee: form.intakeAssignee,
	};
	const patchIntake = (patch: Partial<IntakeForm>) =>
		setForm((f) => ({
			...f,
			intakeEnabled: patch.enabled ?? f.intakeEnabled,
			intakeRepo: patch.repo ?? f.intakeRepo,
			intakeAssignee: patch.assignee ?? f.intakeAssignee,
		}));
	const effectiveIntakeRepo = form.intakeRepo.trim() || deriveRepoPath(project.repo);
	const reviewerWarning = reviewerTrustWarning(form.reviewerHarness);
	const mutation = useMutation({
		mutationFn: async () => {
			const displayName = form.displayName.trim();
			const {
				model: _legacyModel,
				mode: _legacyMode,
				effort: _legacyEffort,
				permissions: _legacyPermissions,
				...sharedAgentConfig
			} = config.agentConfig ?? {};
			const existingReviewer = config.reviewers?.[0];
			const existingReviewerAgentConfig =
				existingReviewer?.harness === form.reviewerHarness ? existingReviewer.agentConfig : undefined;
			const next: ProjectConfig = isScratchProject
				? {
						...scratchSupportedConfig(config),
						worker: {
							...config.worker,
							agent: form.workerAgent,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, form.workerModel, form.workerMode, "", form.workerPermissions),
						},
						manager: {
							...config.manager,
							agent: form.managerAgent,
							agentConfig: buildRoleAgentConfig(
								config.manager?.agentConfig,
								form.managerModel,
								form.managerMode,
								"",
								form.managerPermissions,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: undefined,
						}),
					}
				: {
						...config,
						defaultBranch:
							form.defaultBranch.trim() === DEFAULT_BRANCH_AUTO
								? undefined
								: form.defaultBranch || undefined,
						sessionPrefix: form.sessionPrefix || undefined,
						worker: {
							...config.worker,
							agent: form.workerAgent,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, form.workerModel, form.workerMode, "", form.workerPermissions),
						},
						manager: {
							...config.manager,
							agent: form.managerAgent,
							agentConfig: buildRoleAgentConfig(
								config.manager?.agentConfig,
								form.managerModel,
								form.managerMode,
								"",
								form.managerPermissions,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: undefined,
						}),
						reviewers: form.reviewerHarness
							? [{
									harness: form.reviewerHarness,
									agentConfig: buildRoleAgentConfig(existingReviewerAgentConfig, form.reviewerModel, form.reviewerMode, "", form.reviewerPermissions),
								}]
							: undefined,
						trackerIntake: buildIntake(intakeForm, config.trackerIntake),
						autoReview: form.autoReview,
					};
			const { error } = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
				body: { displayName, config: next },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (
				form.managerAgent !== initialManagerAgent ||
				(activeManager && activeManager.provider !== form.managerAgent)
			) {
				try {
					const sessionId = await spawnManager(projectId, "settings", true);
					return {
						replacementError: null,
						replacementSessionId: sessionId,
						replacementFailure: null,
						spawnError: null,
					} satisfies SettingsSaveResult;
				} catch (error) {
					const replacementFailure: ManagerReplacementFailure = {
						message:
							error instanceof Error ? error.message : "Could not replace manager",
						...(error instanceof ManagerSpawnError
							? { code: error.code, requestId: error.requestId }
							: {}),
					};
					return {
						replacementError: replacementFailure.message,
						replacementSessionId: null,
						replacementFailure,
						spawnError: error,
					} satisfies SettingsSaveResult;
				}
			}
			return {
				replacementError: null,
				replacementSessionId: null,
				replacementFailure: null,
				spawnError: null,
			} satisfies SettingsSaveResult;
		},
		onSuccess: async (result) => {
			setSavedAt(Date.now());
			setReplacementError(result.replacementError);
			setValidationError(null);
			void queryClient.invalidateQueries({ queryKey: ["project", projectId] });
			const workspaceRefresh = onSaved();

			if (result.replacementSessionId) {
				await workspaceRefresh;
				closeSettings();
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId, sessionId: result.replacementSessionId },
				});
				return;
			}

			if (result.replacementFailure) {
				closeSettings();
				setManagerReplacementError(projectId, result.replacementFailure);
			}
		},
	});

	useEffect(() => {
		if (!mutation.isPending) {
			setShowSaving(false);
			return;
		}
		const timeout = window.setTimeout(() => setShowSaving(true), 200);
		return () => window.clearTimeout(timeout);
	}, [mutation.isPending]);

	useEffect(() => {
		const mutationError = mutation.isError
			? mutation.error instanceof Error
				? mutation.error.message
				: "Save failed"
			: undefined;
		onSaveState?.({
			phase: validationError || mutationError
				? "failed"
				: mutation.isPending
					? showSaving
						? "saving"
						: "pending"
					: savedAt !== null
						? "saved"
						: "idle",
			error: validationError ?? mutationError,
			replacementError: !mutation.isPending && !mutation.isError ? replacementError ?? undefined : undefined,
		});
	}, [
		mutation.error,
		mutation.isError,
		mutation.isPending,
		onSaveState,
		replacementError,
		savedAt,
		showSaving,
		
		validationError,
	]);

	useEffect(() => {
		if (savedAt === null) return;
		const timeout = window.setTimeout(() => setSavedAt(null), 1800);
		return () => window.clearTimeout(timeout);
	}, [savedAt]);

	return (
		<ProjectSettingsFormView
			id="project-settings-form"
				onSubmit={() => {
				setSavedAt(null);
				setReplacementError(null);
				const validation = validateProjectSettings(form, { validateIntake: !isScratchProject });
				if (validation) {
					setValidationError(
						validation === "agents_required"
							? "Worker and manager agents are required."
							: validation === "name_required"
								? "Project name is required."
								: "Enabling intake requires an assignee.",
					);
					return;
				}
				if (!tuningValidity.worker || !tuningValidity.manager || !tuningValidity.reviewer) {
					setValidationError("Choose supported model tuning values before saving.");
					return;
				}
				setValidationError(null);
				mutation.mutate();
			}}
		>
			{section === "general" && (
				<>
					<ProjectGeneralSettingsView
						displayName={form.displayName}
						externalLink={ProductExternalLink}
						icons={{
							edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
						}}
						onDisplayNameChange={(displayName) => setForm((f) => ({ ...f, displayName }))}
						labels={{
							title: "Identity",
							name: "Project name",
							id: "id",
							kind: "Type",
							path: "path",
							repo: "repo",
							workspaceRepos: "Workspace repos",
							workspaceReposEmpty: "No child repositories are registered.",
							editName: `Edit ${"Project name"}`,
						}}
						project={{
							id: project.id,
							kindLabel: projectKindLabel(project.kind),
							path: project.path,
							pathHref: `file://${encodeURI(project.path)}`,
							repo: project.repo,
							repoHref: project.repo ? repositoryHref(project.repo) : undefined,
							workspaceRepos: project.kind === "workspace" ? project.workspaceRepos ?? [] : undefined,
						}}
					/>
				</>
			)}

			{section === "agents" && (
				<>
					<ProjectAgentsSettingsView
						title="Agents"
						workerArea={
							<RequiredAgentField
								id="workerAgent"
								variant="settings-row"
								value={form.workerAgent}
								placeholder="Select worker agent"
								label="Default worker agent"
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && form.workerAgent === ""}
								onChange={(v) =>
									setForm((f) => ({ ...f, workerAgent: v, workerModel: "", workerMode: "" }))
								}
							/>
						}
						workerModelArea={
							<AgentModelField
								role="worker"
								agentId={form.workerAgent}
								projectId={projectId}
								model={form.workerModel}
								mode={form.workerMode}
								onModelChange={(workerModel) => setForm((f) => ({ ...f, workerModel }))}
								onModeChange={(workerMode) => setForm((f) => ({ ...f, workerMode }))}
							/>
						}
						managerArea={
							<RequiredAgentField
								id="managerAgent"
								variant="settings-row"
								value={form.managerAgent}
								placeholder="Select manager agent"
								label="Default manager agent"
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && form.managerAgent === ""}
								onChange={(v) =>
									setForm((f) => ({
										...f,
										managerAgent: v,
										managerModel: "",
										managerMode: "",
										managerEffort: "",
									}))
								}
							/>
						}
						managerModelArea={
							<AgentModelField
								role="manager"
								agentId={form.managerAgent}
								projectId={projectId}
								model={form.managerModel}
								mode={form.managerMode}
								onModelChange={(managerModel) => setForm((f) => ({ ...f, managerModel }))}
								onModeChange={(managerMode) => setForm((f) => ({ ...f, managerMode }))}
							/>
						}
						permissions={{
							control: (
								<PermissionModeSelect
									ariaLabel={`${"Worker"} approval`}
									value={form.workerPermissions}
									onChange={(v) => setForm((f) => ({ ...f, workerPermissions: v }))}
								/>
							),
							label: `${"Worker"} approval`,
						}}
						managerPermissions={{
							control: <PermissionModeSelect ariaLabel={`${"Manager"} approval`} value={form.managerPermissions} onChange={(v) => setForm((f) => ({ ...f, managerPermissions: v }))} />,
							label: `${"Manager"} approval`,
						}}
						missingRequiredMessage={
							missingRequiredAgent ? "Worker and manager agents are required." : null
						}
					/>
				{!isScratchProject && (
					<ProjectSettingsSection title="Reviewer" grouped>
						<SettingsRow label="Default reviewer agent">
							<ReviewerSelect
								value={form.reviewerHarness}
								model={form.reviewerModel}
								mode={form.reviewerMode}
								projectId={projectId}
								onConfigChange={(_harness, config) => setForm((f) => ({
									...f,
									reviewerModel: config.model ?? "",
									reviewerMode: config.mode ?? "",
								}))}
								onChange={(v) =>
								setForm((f) => ({
									...f,
									reviewerHarness: v,
									...(v !== f.reviewerHarness ? {
										reviewerModel: "", reviewerMode: "",
										reviewerPermissions: "",
									} : {}),
									}))
								}
								ariaLabel="Default reviewer agent"
								agents={agentCatalog?.agents}
								defaultOptionLabel="Project default"
								defaultTriggerLabel="Project default"
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
							/>
						</SettingsRow>
						{form.reviewerHarness ? (
							<AgentModelField
								role="reviewer"
								agentId={form.reviewerHarness}
								projectId={projectId}
								model={form.reviewerModel}
								mode={form.reviewerMode}
								onModelChange={(reviewerModel) => setForm((f) => ({ ...f, reviewerModel }))}
								onModeChange={(reviewerMode) => setForm((f) => ({ ...f, reviewerMode }))}
							/>
						) : null}
						<SettingsRow label={`${"Reviewer"} approval`}>
							<PermissionModeSelect
								ariaLabel={`${"Reviewer"} approval`}
								value={form.reviewerPermissions}
								onChange={(reviewerPermissions) => setForm((f) => ({ ...f, reviewerPermissions }))}
							/>
						</SettingsRow>
						{reviewerWarning && (
							<p className="px-1 text-xs leading-row text-warning" role="status">
								{reviewerWarning}
							</p>
						)}
						<div className="settings-row-bar">
							<div className="flex shrink-0 items-center gap-1.5">
								<span className="whitespace-nowrap text-sm leading-5 text-settings-label">
									{"Auto review PRs"}
								</span>
								<Tooltip>
									<TooltipTrigger asChild>
										<button
											type="button"
											className="inline-flex size-5 items-center justify-center rounded-md text-settings-muted transition-colors hover:bg-settings-menu-selected hover:text-settings-label focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
											aria-label="When enabled, new worker sessions will automatically review their pull requests. This can still be toggled per session after spawn."
										>
											<Info className="size-icon-sm" aria-hidden="true" />
										</button>
									</TooltipTrigger>
									<TooltipContent className="max-w-72 leading-normal" side="top">
										{"When enabled, new worker sessions will automatically review their pull requests. This can still be toggled per session after spawn."}
									</TooltipContent>
								</Tooltip>
							</div>
							<div className="flex min-w-0 flex-1 items-center justify-end">
								<Switch
									aria-label="Auto review PRs"
									checked={form.autoReview}
									id="project-auto-review"
									onCheckedChange={(checked) => setForm((f) => ({ ...f, autoReview: checked }))}
								/>
							</div>
						</div>
					</ProjectSettingsSection>
				)}
				</>
			)}

			{section === "workflow" && (
				<>
					{!isScratchProject ? (
						<>
							<ProjectWorkflowSettingsView
								branch={form.defaultBranch}
								icons={{
									edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
								}}
								prefix={form.sessionPrefix}
								onBranchChange={(defaultBranch) => setForm((f) => ({ ...f, defaultBranch }))}
								onPrefixChange={(sessionPrefix) => setForm((f) => ({ ...f, sessionPrefix }))}
								labels={{
									worktrees: "Worktrees",
									defaultBranch: "Default branch",
									sessionPrefix: "Session prefix",
									reviewers: "Reviewers",
									defaultReviewer: "Default reviewer agent",
									editDefaultBranch: `Edit ${"Default branch"}`,
									editSessionPrefix: `Edit ${"Session prefix"}`,
								}}
							/>
						</>
					) : (
						<p className="px-1 text-xs text-settings-muted">{"Workflow"}</p>
					)}
				</>
			)}

			{section === "intake" && (
				<>
					{!isScratchProject ? (
						<ProjectSettingsSection title="Tracker intake" grouped>
							<IntakeFields
								variant="settings"
								form={intakeForm}
								onChange={patchIntake}
								repoPreview={{ value: effectiveIntakeRepo, host: deriveRepoHost(project.repo) }}
							/>
						</ProjectSettingsSection>
					) : (
						<p className="px-1 text-xs text-settings-muted">{"Tracker intake"}</p>
					)}
				</>
			)}
		</ProjectSettingsFormView>
	);
}

function AgentModelField({
	role,
	agentId,
	projectId,
	model,
	mode,
	onModelChange,
	onModeChange,
	onValidityChange,
}: {
	role: "worker" | "manager" | "reviewer";
	agentId: string;
	projectId: string;
	model: string;
	mode: string;
	onModelChange: (value: string) => void;
	onModeChange: (value: string) => void;
}) {
	const queryClient = useQueryClient();
	const query = useQuery(agentModelsQueryOptions(agentId, projectId));
	const catalog: AgentModelCatalog | undefined = query.data;
	const revalidationQuery = useQuery({
		queryKey: ["agent-model-revalidation", agentId, projectId, catalog?.validatedAt ?? ""],
		queryFn: () => revalidateAgentModels(agentId, projectId),
		enabled: agentId !== "" && catalog?.refreshRecommended === true,
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
	});
	useEffect(() => {
		if (revalidationQuery.data) {
			queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), revalidationQuery.data);
		}
	}, [agentId, projectId, queryClient, revalidationQuery.data]);
	const isMode = catalog?.selectionMode === "mode";
	const label = {
		workerMode: "Worker mode",
		workerModel: "Worker model",
		managerMode: "Manager mode",
		managerModel: "Manager model",
		reviewerMode: "Reviewer mode",
		reviewerModel: "Reviewer model",
	}[`${role}${isMode ? "Mode" : "Model"}`] ?? role;
	const warning =
		(revalidationQuery.isError
			? revalidationQuery.error instanceof Error
				? revalidationQuery.error.message
				: "Could not validate cached models."
			: undefined) ??
		catalog?.warning ??
		(query.isError ? (query.error instanceof Error ? query.error.message : "Could not load models.") : undefined);

	if (agentId !== "" && query.isFetching && catalog === undefined) {
		return (
			<SettingsRow label={label}>
				<span className="text-xs text-settings-muted" role="status" aria-label="Loading models…">
					{"Loading models…"}
				</span>
			</SettingsRow>
		);
	}

	if (isMode) {
		const options = [
			{ value: "__default__", label: "Agent default" },
			...(catalog.models ?? []).map((item) => ({ value: item.id, label: item.label })),
		];
		return (
			<>
				<SettingsRow label={label}>
					<div className="flex min-w-0 items-center gap-2">
						<SettingsOptionMenu
							aria-label={label}
							value={mode || "__default__"}
							options={options}
							triggerClassName="justify-end"
							onChange={(value) => {
								onModeChange(value === "__default__" ? "" : value);
								onModelChange("");
							}}
						/>
					</div>
				</SettingsRow>
				{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
			</>
		);
	}

	const customModelEntry = catalog?.customModelEntry ?? (catalog?.allowCustom ? "direct" : "none");
	const refreshCatalog = async () => {
		const refreshed = await refreshAgentModels(agentId, projectId);
		queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), refreshed);
	};
	const selectCatalogModel = (value: string) => {
		onModelChange(value);
		onModeChange("");
	};
	const selectCustomModel = (value: string) => {
		onModelChange(value);
		onModeChange("");
	};
	return (
		<>
			<SettingsRow label={label}>
				<div className="flex min-w-0 items-center gap-2">
					<AgentModelCombobox
						aria-label={label}
						value={model}
						models={catalog?.models ?? []}
						allowCustom={catalog?.allowCustom}
						customModelEntry={customModelEntry}
						agentLabel={agentId}
						onRefresh={refreshCatalog}
						disabled={query.isFetching || agentId === ""}
						onChange={selectCatalogModel}
						onCustom={selectCustomModel}
						triggerClassName="justify-end"
						compact={agentId === "opencode"}
						tuning={agentId === "opencode" ? {
							onValidityChange,
							roleLabel: ({"worker": "Worker", "manager": "Manager", "reviewer": "Reviewer"}[role] ?? role),
						} : undefined}
					/>
				</div>
			</SettingsRow>
			{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
		</>
	);
}

function PermissionModeSelect({ ariaLabel, value, onChange }: { ariaLabel: string; value: string; onChange: (value: string) => void }) {
	const options = [
		{ value: "__default__", label: `${"Auto"} (${"Project default"})` },
		...PERMISSION_MODE_VALUES.map((value) => ({
			value,
			label:
				value === "default"
					? "Default"
					: value === "accept-edits"
						? "Accept edits"
						: value === "auto"
							? "Auto"
							: "Bypass permissions",
		})),
	];

	return (
		<SettingsOptionMenu
			aria-label={ariaLabel}
			value={value || "__default__"}
			options={options}
			onChange={(v) => onChange(v === "__default__" ? "" : v)}
		/>
	);
}

function projectKindLabel(kind: string): string {
	switch (kind) {
		case "single_repo":
			return "Git repository";
		case "workspace":
			return "Workspace";
		case "scratch":
			return "Scratch project";
		default:
			return kind || "Unknown";
	}
}

function repositoryHref(repository: string): string {
	if (/^https?:\/\//i.test(repository)) return repository;
	if (repository.startsWith("git@")) {
		const [host, path] = repository.slice(4).split(":", 2);
		return `https://${host}/${path.replace(/\.git$/, "")}`;
	}
	if (repository.startsWith("ssh://")) {
		try {
			const parsed = new URL(repository);
			return `https://${parsed.hostname}${parsed.pathname.replace(/\.git$/, "")}`;
		} catch {
			return repository;
		}
	}
	return repository;
}

function scratchSupportedConfig(config: ProjectConfig): ProjectConfig {
	const {
		defaultBranch: _defaultBranch,
		reviewers: _reviewers,
		autoReview: _legacyAutoReview,
		trackerIntake: _trackerIntake,
		...supported
	} = config as ProjectConfig;
	return supported;
}

function blankToUndefined<T extends object>(obj: T): T | undefined {
	return Object.values(obj).some((v) => v !== undefined) ? obj : undefined;
}

function buildRoleAgentConfig(
	existing: components["schemas"]["AgentConfig"] | undefined,
	model: string,
	mode: string,
	permissions: string,
): components["schemas"]["AgentConfig"] | undefined {
	const next = { ...existing };
	if (model) next.model = model;
	else delete next.model;
	if (mode) next.mode = mode;
	else delete next.mode;
	if (permissions) next.permissions = permissions as components["schemas"]["AgentConfig"]["permissions"];
	else delete next.permissions;
	return Object.keys(next).length > 0 ? next : undefined;
}
