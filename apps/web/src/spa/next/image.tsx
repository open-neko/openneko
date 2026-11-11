import { forwardRef, type ImgHTMLAttributes } from "react";

type ImageProps = Omit<ImgHTMLAttributes<HTMLImageElement>, "src" | "alt"> & {
  src: string | { src: string };
  alt: string;
  priority?: boolean;
};

const Image = forwardRef<HTMLImageElement, ImageProps>(function Image(
  { src, alt, priority, loading, ...rest },
  ref,
) {
  return <img ref={ref} src={typeof src === "string" ? src : src.src} alt={alt} loading={loading ?? (priority ? "eager" : "lazy")} {...rest} />;
});

export default Image;
