// The pages that read only the SDK, by the id that `?page=` names.
import type { Child } from "@reactre/core/jsx-runtime";

import { icons } from "../icons.ts";
import { CostPage } from "./cost/cost.tsx";
import { NewRoutePage } from "./new-route/new-route.tsx";
import { ProjectsPage } from "./projects/projects.tsx";
import { RoutesPage } from "./route/route.tsx";
import { SuggestionsPage } from "./suggestions/suggestions.tsx";
import { TasksPage } from "./tasks/tasks.tsx";
import { TracesPage } from "./traces/traces.tsx";
import { WorktreesPage } from "./worktrees/worktrees.tsx";

export interface PageEntry {
  id: string;
  label: string;
  icon: string[];
  view: () => Child;
}

export const pages: readonly PageEntry[] = [
  { id: "projects", label: "Projects", icon: icons.folder, view: ProjectsPage },
  { id: "tasks", label: "Tasks", icon: icons.check, view: TasksPage },
  { id: "route", label: "Route starts", icon: icons.play, view: RoutesPage },
  { id: "worktrees", label: "Worktrees", icon: icons.branch, view: WorktreesPage },
  { id: "suggestions", label: "Suggestions", icon: icons.search, view: SuggestionsPage },
  { id: "cost", label: "Cost", icon: icons.activity, view: CostPage },
  { id: "traces", label: "Traces", icon: icons.clock, view: TracesPage },
  { id: "new-route", label: "New route", icon: icons.plus, view: NewRoutePage },
];

export function pageById(id: string): PageEntry | undefined {
  return pages.find((page) => page.id === id);
}
