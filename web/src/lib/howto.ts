/* HowTo structured data for a recipe page. The step texts are the visible
 * steps' facts joined, so the markup cannot describe anything the page does
 * not say. No totalTime and no video: neither is promised on the page. */
import type { Fact, Step } from "./facts";

export interface Recipe {
  slug: string;
  route: string;
  title: string;
  description: string;
  heading: string;
  accent: string;
  lede: string;
  tag: string;
  needs: Fact[];
  steps: Step[];
  check: Fact[];
  docs: string[];
  howto: { name: string; tools: string[]; supplies: string[] };
}

export function howToLd(r: Recipe, site = "https://polyemesis.com"): object {
  return {
    "@context": "https://schema.org",
    "@type": "HowTo",
    name: r.howto.name,
    description: r.description,
    tool: r.howto.tools.map((name) => ({ "@type": "HowToTool", name })),
    supply: r.howto.supplies.map((name) => ({ "@type": "HowToSupply", name })),
    step: r.steps.map((s, i) => ({
      "@type": "HowToStep",
      name: s.title.replace(/\.$/, ""),
      text: s.facts.map((f) => f.text).join(" "),
      url: `${site}${r.route}#step-${i + 1}`,
    })),
  };
}
