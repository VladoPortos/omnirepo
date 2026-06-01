/**
 * 404 page per UI spec General copywriting.
 */

import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/button';

export function NotFoundPage() {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background p-4 text-center">
      <h1 className="text-4xl font-semibold text-foreground mb-2">
        Page Not Found
      </h1>
      <p className="text-muted-foreground mb-6 max-w-md">
        The page you're looking for doesn't exist or has been moved.
      </p>
      <Button nativeButton={false} render={<Link to="/" />}>Go to Dashboard</Button>
    </div>
  );
}
