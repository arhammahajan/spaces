import type { ScreenReading } from "./arbitrate";
import { defaultDetectionRegistry, type DetectionRegistry } from "./policies";
import { loadBundledScreenEngine, type ScreenEngine } from "./screen";
import { CUSTOM_TITLE_EXTRACTORS, extractGenericTitle } from "./titles";

export * from "./arbitrate";
export * from "./policies";
export * from "./regex";
export {
  codexBlockMarkerLine,
  codexPromptLine,
  currentCodexPromptIndex,
  extractRegion,
  isHorizontalRule,
  promptBoxTopBorderIndex,
  splitTextLines,
  topRegionCount,
  validateRegionSpec,
  type DetectionInput,
  type TextLine,
} from "./regions";
export * from "./screen";
export * from "./titles";

/**
 * Evaluates a pane's viewport + OSC title against the bundled Herdr manifest
 * engine and registered title extractors, producing a `ScreenReading` ready
 * for `Arbitrator.apply`.
 */
export function detectScreenReading(
  app: string,
  viewport: string,
  oscTitle = "",
  registry: DetectionRegistry = defaultDetectionRegistry(),
  engine: ScreenEngine = loadBundledScreenEngine(),
): ScreenReading {
  const spec = registry.resolveRuntime(app);
  const manifestId = spec?.screenManifestId ?? "";
  const detected =
    manifestId !== ""
      ? engine.detect(manifestId, viewport, oscTitle)
      : engine.detect(app, viewport, oscTitle);

  const customExtractor = spec ? CUSTOM_TITLE_EXTRACTORS[spec.id] : undefined;
  const title =
    viewport.trim() === ""
      ? ""
      : customExtractor
        ? customExtractor(viewport)
        : extractGenericTitle(viewport);

  return {
    app,
    state: detected.state,
    visible_idle: detected.visibleIdle,
    visible_blocker: detected.visibleBlocker,
    visible_working: detected.visibleWorking,
    skip_state_update: detected.skipStateUpdate,
    osc_authority: spec ? spec.hookPolicy.oscAuthority() : registry.isOscAuthority(app),
    title,
  };
}
