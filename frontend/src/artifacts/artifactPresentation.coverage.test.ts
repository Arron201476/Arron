import { describe, expect, it } from "vitest";
import commonSchema from "../../../schemas/v1/common.schema.json";
import nonNovelSchema from "../../../schemas/v1/non-novel-to-script.schema.json";
import novelSchema from "../../../schemas/v1/novel-to-script.schema.json";
import qualityReviewBatchSchema from "../../../schemas/v1/quality-review-batch.schema.json";
import qualityReviewSchema from "../../../schemas/v1/quality-review.schema.json";
import videoSchema from "../../../schemas/v1/video-reference-creation.schema.json";
import { fieldLabels, isTechnicalField, scalarLabels } from "./artifactPresentation";

const schemas = {
  common: commonSchema,
  novel: novelSchema,
  nonNovel: nonNovelSchema,
  video: videoSchema,
  qualityReview: qualityReviewSchema,
  qualityReviewBatch: qualityReviewBatchSchema,
};

describe("artifact presentation coverage", () => {
  it.each(Object.entries(schemas))("maps every visible field in %s to a Chinese label", (_, schema) => {
    const { fields } = collectSchemaPresentationValues(schema);
    const missing = [...fields].filter((key) => !isTechnicalField(key) && !fieldLabels[key]).sort();
    expect(missing).toEqual([]);
  });

  it.each(Object.entries(schemas))("maps every machine enum in %s to a Chinese label", (_, schema) => {
    const { enumValues } = collectSchemaPresentationValues(schema);
    const missing = [...enumValues]
      .filter((value) => /[A-Za-z]/.test(value) && !scalarLabels[value])
      .sort();
    expect(missing).toEqual([]);
  });
});

function collectSchemaPresentationValues(root: unknown) {
  const fields = new Set<string>();
  const enumValues = new Set<string>();
  const visit = (value: unknown) => {
    if (Array.isArray(value)) {
      value.forEach(visit);
      return;
    }
    if (!isRecord(value)) return;
    if (isRecord(value.properties)) Object.keys(value.properties).forEach((key) => fields.add(key));
    if (Array.isArray(value.enum)) value.enum.forEach((item) => { if (typeof item === "string") enumValues.add(item); });
    Object.values(value).forEach(visit);
  };
  visit(root);
  return { fields, enumValues };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
