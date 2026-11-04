import "@testing-library/jest-dom/vitest";

// jsdom has no canvas; viewer code skips drawing when there is no 2D context.
HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement["getContext"];
