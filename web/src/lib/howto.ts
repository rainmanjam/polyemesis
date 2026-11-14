/* HowTo structured data for a recipe page. The step texts are the visible
 * steps' facts joined, so the markup cannot describe anything the page does
 * not say: the name is the page's h1, and there is no tool or supply list, no
 * totalTime and no video, because none of those is shown on the page. */
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
}

export function howToLd(r: Recipe, site = "https://polyemesis.com"): object {
  return {
    "@context": "https://schema.org",
    "@type": "HowTo",
    name: r.heading,
    description: r.description,
    step: r.steps.map((s, i) => ({
      "@type": "HowToStep",
      name: s.title.replace(/\.$/, ""),
      text: s.facts.map((f) => f.text).join(" "),
      url: `${site}${r.route}#step-${i + 1}`,
    })),
  };
}
